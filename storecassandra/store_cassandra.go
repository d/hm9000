package storecassandra

import (
	"github.com/cloudfoundry/hm9000/config"
	"github.com/cloudfoundry/hm9000/helpers/timeprovider"
	"github.com/cloudfoundry/hm9000/models"
	"github.com/cloudfoundry/hm9000/store"
	"time"
	"tux21b.org/v1/gocql"
)

type StoreCassandra struct {
	session      *gocql.Session
	conf         config.Config
	timeProvider timeprovider.TimeProvider
}

func New(conf config.Config, timeProvider timeprovider.TimeProvider) (*StoreCassandra, error) {
	s := &StoreCassandra{
		conf:         conf,
		timeProvider: timeProvider,
	}

	cluster := gocql.NewCluster("127.0.0.1")
	cluster.DefaultPort = 9042
	cluster.Consistency = gocql.One
	cluster.NumConns = 1
	cluster.NumStreams = 1
	var err error

	s.session, err = cluster.CreateSession()
	if err != nil {
		println("FAILED TO CREATE SESSION")
		return s, err
	}

	err = s.session.Query(`CREATE KEYSPACE IF NOT EXISTS hm9000 WITH REPLICATION = { 'class' : 'SimpleStrategy', 'replication_factor' : 1 }`).Exec()
	if err != nil {
		println("FAILED TO CREATE KEYSPACE")
		return s, err
	}
	s.session.Close()

	cluster.Keyspace = "hm9000"
	s.session, err = cluster.CreateSession()

	err = s.session.Query(`CREATE TABLE IF NOT EXISTS DesiredStates (app_guid text, app_version text, number_of_instances int, memory int, state text, package_state text, updated_at bigint, expires bigint, PRIMARY KEY (app_guid, app_version))`).Exec()
	if err != nil {
		println("FAILED TO CREATE TABLE DesiredStates")
		return s, err
	}

	err = s.session.Query(`CREATE TABLE IF NOT EXISTS ActualStates (app_guid text, app_version text, instance_guid text, instance_index int, state text, state_timestamp bigint, cc_partition text, expires bigint, PRIMARY KEY (app_guid, app_version, instance_guid))`).Exec()
	if err != nil {
		println("FAILED TO CREATE TABLE ActualStates")
		return s, err
	}

	err = s.session.Query(`CREATE TABLE IF NOT EXISTS CrashCounts (app_guid text, app_version text, instance_index int, crash_count int, created_at bigint, expires bigint, PRIMARY KEY (app_guid, app_version, instance_index))`).Exec()
	if err != nil {
		println("FAILED TO CREATE TABLE CrashCounts")
		return s, err
	}

	err = s.session.Query(`CREATE TABLE IF NOT EXISTS PendingStartMessages (app_guid text, app_version text, message_id text, send_on bigint, sent_on bigint, keep_alive int, index_to_start int, priority double, skip_verification boolean, PRIMARY KEY (app_guid, app_version, index_to_start))`).Exec()
	if err != nil {
		println("FAILED TO CREATE TABLE PendingStartMessages")
		return s, err
	}

	err = s.session.Query(`CREATE TABLE IF NOT EXISTS PendingStopMessages (app_guid text, app_version text, message_id text, send_on bigint, sent_on bigint, keep_alive int, instance_guid text, PRIMARY KEY (app_guid, app_version, instance_guid))`).Exec()
	if err != nil {
		println("FAILED TO CREATE TABLE PendingStopMessages")
		return s, err
	}

	err = s.session.Query(`CREATE TABLE IF NOT EXISTS Freshness (key text, created_at bigint, expires bigint, PRIMARY KEY (key))`).Exec()
	if err != nil {
		println("FAILED TO CREATE TABLE Freshness")
		return s, err
	}

	return s, err
}

func (s *StoreCassandra) SaveDesiredState(desiredStates ...models.DesiredAppState) error {
	for _, state := range desiredStates {
		err := s.session.Query(`INSERT INTO DesiredStates (app_guid, app_version, number_of_instances, memory, state, package_state, updated_at, expires) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, state.AppGuid, state.AppVersion, state.NumberOfInstances, state.Memory, state.State, state.PackageState, int64(state.UpdatedAt.Unix()), s.timeProvider.Time().Unix()+int64(s.conf.DesiredStateTTL())).Exec()
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *StoreCassandra) GetDesiredState() (map[string]models.DesiredAppState, error) {
	result := map[string]models.DesiredAppState{}
	var err error

	iter := s.session.Query(`SELECT app_guid, app_version, number_of_instances, memory, state, package_state, updated_at, expires FROM DesiredStates`).Iter()

	var appGuid, appVersion, state, packageState string
	var numberOfInstances, memory int32
	var updatedAt, expires int64

	currentTime := s.timeProvider.Time().Unix()

	for iter.Scan(&appGuid, &appVersion, &numberOfInstances, &memory, &state, &packageState, &updatedAt, &expires) {
		if expires <= currentTime {
			err = s.deleteDesiredState(appGuid, appVersion)
			if err != nil {
				return result, err
			}
		} else {
			desiredState := models.DesiredAppState{
				AppGuid:           appGuid,
				AppVersion:        appVersion,
				NumberOfInstances: int(numberOfInstances),
				Memory:            int(memory),
				State:             models.AppState(state),
				PackageState:      models.AppPackageState(packageState),
				UpdatedAt:         time.Unix(updatedAt, 0),
			}
			result[desiredState.StoreKey()] = desiredState
		}
	}

	err = iter.Close()

	return result, err
}

func (s *StoreCassandra) deleteDesiredState(appGuid string, appVersion string) error {
	return s.session.Query(`DELETE FROM DesiredStates WHERE app_guid=? AND app_version=?`, appGuid, appVersion).Exec()
}

func (s *StoreCassandra) DeleteDesiredState(desiredStates ...models.DesiredAppState) error {
	for _, state := range desiredStates {
		err := s.deleteDesiredState(state.AppGuid, state.AppVersion)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *StoreCassandra) SaveActualState(actualStates ...models.InstanceHeartbeat) error {
	for _, state := range actualStates {
		err := s.session.Query(`INSERT INTO ActualStates (app_guid, app_version, instance_guid, instance_index, state, state_timestamp, cc_partition, expires) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			state.AppGuid,
			state.AppVersion,
			state.InstanceGuid,
			int32(state.InstanceIndex),
			state.State,
			int64(state.StateTimestamp),
			state.CCPartition,
			s.timeProvider.Time().Unix()+int64(s.conf.HeartbeatTTL())).Exec()
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *StoreCassandra) GetActualState() (map[string]models.InstanceHeartbeat, error) {
	result := map[string]models.InstanceHeartbeat{}
	var err error

	iter := s.session.Query(`SELECT app_guid, app_version, instance_guid, instance_index, state, state_timestamp, cc_partition, expires FROM ActualStates`).Iter()

	var appGuid, appVersion, instanceGuid, state, ccPartition string
	var instanceIndex int32
	var stateTimestamp, expires int64

	currentTime := s.timeProvider.Time().Unix()

	for iter.Scan(&appGuid, &appVersion, &instanceGuid, &instanceIndex, &state, &stateTimestamp, &ccPartition, &expires) {
		if expires <= currentTime {
			err := s.session.Query(`DELETE FROM ActualStates WHERE app_guid=? AND app_version=? AND instance_guid = ?`, appGuid, appVersion, instanceGuid).Exec()
			if err != nil {
				return result, err
			}
		} else {
			actualState := models.InstanceHeartbeat{
				CCPartition:    ccPartition,
				AppGuid:        appGuid,
				AppVersion:     appVersion,
				InstanceGuid:   instanceGuid,
				InstanceIndex:  int(instanceIndex),
				State:          models.InstanceState(state),
				StateTimestamp: float64(stateTimestamp),
			}
			result[actualState.StoreKey()] = actualState
		}
	}

	err = iter.Close()

	return result, err
}

func (s *StoreCassandra) SaveCrashCounts(crashCounts ...models.CrashCount) error {
	for _, crashCount := range crashCounts {
		err := s.session.Query(`INSERT INTO CrashCounts (app_guid, app_version, instance_index, crash_count, created_at, expires) VALUES (?, ?, ?, ?, ?, ?)`,
			crashCount.AppGuid,
			crashCount.AppVersion,
			int32(crashCount.InstanceIndex),
			int32(crashCount.CrashCount),
			crashCount.CreatedAt,
			s.timeProvider.Time().Unix()+int64(s.conf.MaximumBackoffDelay().Seconds()*2)).Exec()
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *StoreCassandra) GetCrashCounts() (map[string]models.CrashCount, error) {
	result := map[string]models.CrashCount{}
	var err error

	iter := s.session.Query(`SELECT app_guid, app_version, instance_index, crash_count, created_at, expires FROM CrashCounts`).Iter()

	var appGuid, appVersion string
	var instanceIndex, crashCount int32
	var createdAt, expires int64

	currentTime := s.timeProvider.Time().Unix()

	for iter.Scan(&appGuid, &appVersion, &instanceIndex, &crashCount, &createdAt, &expires) {
		if expires <= currentTime {
			err := s.session.Query(`DELETE FROM CrashCounts WHERE app_guid=? AND app_version=? AND instance_index = ?`, appGuid, appVersion, instanceIndex).Exec()
			if err != nil {
				return result, err
			}
		} else {
			crashCount := models.CrashCount{
				AppGuid:       appGuid,
				AppVersion:    appVersion,
				InstanceIndex: int(instanceIndex),
				CrashCount:    int(crashCount),
				CreatedAt:     createdAt,
			}
			result[crashCount.StoreKey()] = crashCount
		}
	}

	err = iter.Close()

	return result, err
}

func (s *StoreCassandra) SavePendingStartMessages(startMessages ...models.PendingStartMessage) error {
	for _, startMessage := range startMessages {
		err := s.session.Query(`INSERT INTO PendingStartMessages (app_guid, app_version, message_id, send_on, sent_on, keep_alive, index_to_start, priority, skip_verification) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			startMessage.AppGuid,
			startMessage.AppVersion,
			startMessage.MessageId,
			startMessage.SendOn,
			startMessage.SentOn,
			startMessage.KeepAlive,
			startMessage.IndexToStart,
			startMessage.Priority,
			startMessage.SkipVerification).Exec()
		if err != nil {
			return err
		}

	}

	return nil
}

func (s *StoreCassandra) GetPendingStartMessages() (map[string]models.PendingStartMessage, error) {
	startMessages := map[string]models.PendingStartMessage{}
	var err error

	iter := s.session.Query(`SELECT app_guid, app_version, message_id, send_on, sent_on, keep_alive, index_to_start, priority, skip_verification FROM PendingStartMessages`).Iter()

	var messageId, appGuid, appVersion string
	var sendOn, sentOn int64
	var keepAlive, indexToStart int
	var priority float64
	var skipVerification bool

	for iter.Scan(&appGuid, &appVersion, &messageId, &sendOn, &sentOn, &keepAlive, &indexToStart, &priority, &skipVerification) {
		startMessage := models.PendingStartMessage{
			PendingMessage: models.PendingMessage{
				MessageId:  messageId,
				SendOn:     sendOn,
				SentOn:     sentOn,
				KeepAlive:  keepAlive,
				AppGuid:    appGuid,
				AppVersion: appVersion,
			},
			IndexToStart:     indexToStart,
			Priority:         priority,
			SkipVerification: skipVerification,
		}
		startMessages[startMessage.StoreKey()] = startMessage
	}

	err = iter.Close()

	return startMessages, err
}

func (s *StoreCassandra) DeletePendingStartMessages(startMessages ...models.PendingStartMessage) error {
	for _, startMessage := range startMessages {
		err := s.session.Query(`DELETE FROM PendingStartMessages WHERE app_guid=? AND app_version=? AND index_to_start=?`, startMessage.AppGuid, startMessage.AppVersion, startMessage.IndexToStart).Exec()
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *StoreCassandra) SavePendingStopMessages(stopMessages ...models.PendingStopMessage) error {
	for _, stopMessage := range stopMessages {
		err := s.session.Query(`INSERT INTO PendingStopMessages (app_guid, app_version, message_id, send_on, sent_on, keep_alive, instance_guid) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			stopMessage.AppGuid,
			stopMessage.AppVersion,
			stopMessage.MessageId,
			stopMessage.SendOn,
			stopMessage.SentOn,
			stopMessage.KeepAlive,
			stopMessage.InstanceGuid).Exec()
		if err != nil {
			return err
		}

	}

	return nil
}

func (s *StoreCassandra) GetPendingStopMessages() (map[string]models.PendingStopMessage, error) {
	stopMessages := map[string]models.PendingStopMessage{}
	var err error

	iter := s.session.Query(`SELECT app_guid, app_version, message_id, send_on, sent_on, keep_alive, instance_guid FROM PendingStopMessages`).Iter()

	var messageId, appGuid, appVersion, instanceGuid string
	var sendOn, sentOn int64
	var keepAlive int

	for iter.Scan(&appGuid, &appVersion, &messageId, &sendOn, &sentOn, &keepAlive, &instanceGuid) {
		stopMessage := models.PendingStopMessage{
			PendingMessage: models.PendingMessage{
				MessageId:  messageId,
				SendOn:     sendOn,
				SentOn:     sentOn,
				KeepAlive:  keepAlive,
				AppGuid:    appGuid,
				AppVersion: appVersion,
			},
			InstanceGuid: instanceGuid,
		}
		stopMessages[stopMessage.StoreKey()] = stopMessage
	}

	err = iter.Close()

	return stopMessages, err
}

func (s *StoreCassandra) DeletePendingStopMessages(stopMessages ...models.PendingStopMessage) error {
	for _, stopMessage := range stopMessages {
		err := s.session.Query(`DELETE FROM PendingStopMessages WHERE app_guid=? AND app_version=? AND instance_guid=?`,
			stopMessage.AppGuid,
			stopMessage.AppVersion,
			stopMessage.InstanceGuid).Exec()
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *StoreCassandra) BumpDesiredFreshness(timestamp time.Time) error {
	return s.session.Query(`INSERT INTO Freshness (key, created_at, expires) VALUES (?, ?, ?)`, s.conf.DesiredFreshnessKey, timestamp.Unix(), timestamp.Unix()+int64(s.conf.DesiredFreshnessTTL())).Exec()
}

func (s *StoreCassandra) IsDesiredStateFresh() (bool, error) {
	var expires int64
	err := s.session.Query(`SELECT expires FROM Freshness WHERE key=?`, s.conf.DesiredFreshnessKey).Scan(&expires)

	if err == gocql.ErrNotFound {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	if expires <= s.timeProvider.Time().Unix() {
		return false, nil
	}

	return true, nil
}

func (s *StoreCassandra) BumpActualFreshness(timestamp time.Time) error {
	shouldBumpCreatedAt := false
	var expires int64
	err := s.session.Query(`SELECT expires FROM Freshness WHERE key=?`, s.conf.ActualFreshnessKey).Scan(&expires)

	if err == gocql.ErrNotFound {
		shouldBumpCreatedAt = true
	} else if err != nil {
		return err
	} else if expires <= timestamp.Unix() {
		shouldBumpCreatedAt = true
	}

	if shouldBumpCreatedAt {
		err = s.session.Query(`INSERT INTO Freshness (key, created_at) VALUES (?, ?)`, s.conf.ActualFreshnessKey, timestamp.Unix()).Exec()
		if err != nil {
			return err
		}
	}

	err = s.session.Query(`INSERT INTO Freshness (key, expires) VALUES (?, ?)`, s.conf.ActualFreshnessKey, timestamp.Unix()+int64(s.conf.ActualFreshnessTTL())).Exec()
	if err != nil {
		return err
	}

	return nil
}

func (s *StoreCassandra) IsActualStateFresh(timestamp time.Time) (bool, error) {
	var createdAt, expires int64
	err := s.session.Query(`SELECT created_at, expires FROM Freshness WHERE key=?`, s.conf.ActualFreshnessKey).Scan(&createdAt, &expires)

	if err == gocql.ErrNotFound {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	currentTime := s.timeProvider.Time().Unix()

	if createdAt+int64(s.conf.ActualFreshnessTTL()) <= currentTime && currentTime < expires {
		return true, nil
	}

	return false, nil
}

func (s *StoreCassandra) VerifyFreshness(time time.Time) error {
	desiredFresh, err := s.IsDesiredStateFresh()
	if err != nil {
		return err
	}

	actualFresh, err := s.IsActualStateFresh(time)
	if err != nil {
		return err
	}

	if !desiredFresh && !actualFresh {
		return store.ActualAndDesiredAreNotFreshError
	}

	if !desiredFresh {
		return store.DesiredIsNotFreshError
	}

	if !actualFresh {
		return store.ActualIsNotFreshError
	}

	return nil
}
