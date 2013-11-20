package actualstatelistener_test

import (
	"errors"
	. "github.com/cloudfoundry/hm9000/actualstatelistener"
	"github.com/cloudfoundry/yagnats"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"time"

	. "github.com/cloudfoundry/hm9000/models"
	. "github.com/cloudfoundry/hm9000/testhelpers/appfixture"

	"github.com/cloudfoundry/hm9000/config"
	storepackage "github.com/cloudfoundry/hm9000/store"
	"github.com/cloudfoundry/hm9000/testhelpers/fakelogger"
	"github.com/cloudfoundry/hm9000/testhelpers/fakemetricsaccountant"
	"github.com/cloudfoundry/hm9000/testhelpers/fakestoreadapter"
	"github.com/cloudfoundry/hm9000/testhelpers/faketimeprovider"
	"github.com/cloudfoundry/hm9000/testhelpers/fakeusagetracker"
	"github.com/cloudfoundry/yagnats/fakeyagnats"
)

var _ = Describe("Actual state listener", func() {
	var (
		app               AppFixture
		anotherApp        AppFixture
		store             storepackage.Store
		storeAdapter      *fakestoreadapter.FakeStoreAdapter
		listener          *ActualStateListener
		timeProvider      *faketimeprovider.FakeTimeProvider
		messageBus        *fakeyagnats.FakeYagnats
		logger            *fakelogger.FakeLogger
		conf              config.Config
		freshByTime       time.Time
		usageTracker      *fakeusagetracker.FakeUsageTracker
		metricsAccountant *fakemetricsaccountant.FakeMetricsAccountant
	)

	BeforeEach(func() {
		var err error
		conf, err = config.DefaultConfig()
		Ω(err).ShouldNot(HaveOccured())

		timeProvider = &faketimeprovider.FakeTimeProvider{
			TimeToProvide: time.Unix(100, 0),
		}
		freshByTime = time.Unix(int64(100+conf.ActualFreshnessTTL()), 0)

		app = NewAppFixture()
		anotherApp = NewAppFixture()
		anotherApp.DeaGuid = app.DeaGuid

		storeAdapter = fakestoreadapter.New()
		store = storepackage.NewStore(conf, storeAdapter, fakelogger.NewFakeLogger())
		messageBus = fakeyagnats.New()
		logger = fakelogger.NewFakeLogger()

		usageTracker = fakeusagetracker.New()
		usageTracker.UsageToReturn = 0.7
		metricsAccountant = fakemetricsaccountant.New()

		listener = New(conf, messageBus, store, usageTracker, metricsAccountant, timeProvider, logger)
		listener.Start()
	})

	It("should subscribe to the dea.heartbeat subject", func() {
		Ω(messageBus.Subscriptions).Should(HaveKey("dea.heartbeat"))
		Ω(messageBus.Subscriptions["dea.heartbeat"]).Should(HaveLen(1))
	})

	It("should subscribe to the dea.advertise subject", func() {
		Ω(messageBus.Subscriptions).Should(HaveKey("dea.advertise"))
		Ω(messageBus.Subscriptions["dea.advertise"]).Should(HaveLen(1))
	})

	It("should start tracking store usage", func() {
		Ω(usageTracker.DidStart).Should(BeTrue())
		Ω(metricsAccountant.TrackedActualStateListenerStoreUsageFraction).Should(Equal(0.7))
	})

	Context("when the usage tracker is nil", func() {
		It("should not track metrics (or blow up!)", func() {
			metricsAccountant.TrackedActualStateListenerStoreUsageFraction = -1.0

			listener = New(conf, messageBus, store, nil, metricsAccountant, timeProvider, logger)
			Ω(func() {
				listener.Start()
			}).ShouldNot(Panic())

			Ω(metricsAccountant.TrackedActualStateListenerStoreUsageFraction).Should(Equal(-1.0))
		})
	})

	Context("When it receives a dea advertisement over the message bus", func() {
		BeforeEach(func() {
			isFresh, _ := store.IsActualStateFresh(freshByTime)
			Ω(isFresh).Should(BeFalse())
			messageBus.Subscriptions["dea.advertise"][0].Callback(&yagnats.Message{
				Payload: "doesn't matter",
			})
		})

		It("Bumps the actual state freshness", func() {
			isFresh, _ := store.IsActualStateFresh(freshByTime)
			Ω(isFresh).Should(BeTrue())
		})
	})

	Context("When it receives a simple heartbeat over the message bus", func() {
		BeforeEach(func() {
			messageBus.Subscriptions["dea.heartbeat"][0].Callback(&yagnats.Message{
				Payload: string(app.Heartbeat(1).ToJSON()),
			})
		})

		It("puts it in the store", func() {
			foundApp, err := store.GetApp(app.AppGuid, app.AppVersion)
			Ω(err).ShouldNot(HaveOccured())
			Ω(foundApp.InstanceHeartbeats).Should(ContainElement(app.InstanceAtIndex(0).Heartbeat()))
		})
	})

	Context("When it receives a complex heartbeat with multiple apps and instances", func() {
		var heartbeat Heartbeat
		JustBeforeEach(func() {
			isFresh, _ := store.IsActualStateFresh(freshByTime)
			Ω(isFresh).Should(BeFalse())

			heartbeat = Heartbeat{
				DeaGuid: app.DeaGuid,
				InstanceHeartbeats: []InstanceHeartbeat{
					app.InstanceAtIndex(0).Heartbeat(),
					app.InstanceAtIndex(1).Heartbeat(),
					anotherApp.InstanceAtIndex(0).Heartbeat(),
				},
			}

			messageBus.Subscriptions["dea.heartbeat"][0].Callback(&yagnats.Message{
				Payload: string(heartbeat.ToJSON()),
			})
		})

		It("puts it in the store", func() {
			foundApp1, err := store.GetApp(app.AppGuid, app.AppVersion)
			Ω(err).ShouldNot(HaveOccured())

			Ω(foundApp1.InstanceHeartbeats).Should(HaveLen(2))
			Ω(foundApp1.InstanceHeartbeats).Should(ContainElement(app.InstanceAtIndex(0).Heartbeat()))
			Ω(foundApp1.InstanceHeartbeats).Should(ContainElement(app.InstanceAtIndex(1).Heartbeat()))

			foundApp2, err := store.GetApp(anotherApp.AppGuid, anotherApp.AppVersion)
			Ω(err).ShouldNot(HaveOccured())

			Ω(foundApp2.InstanceHeartbeats).Should(ContainElement(anotherApp.InstanceAtIndex(0).Heartbeat()))
		})

		Context("when the save succeeds", func() {
			It("should log about the heartbeat", func() {
				Ω(logger.LoggedSubjects).Should(ContainElement("Synced a Heartbeat"))
				Ω(logger.LoggedMessages).Should(ContainElement(ContainSubstring(`"DEA":"%s"`, heartbeat.DeaGuid)))
			})

			It("bumps the freshness", func() {
				isFresh, _ := store.IsActualStateFresh(freshByTime)
				Ω(isFresh).Should(BeTrue())
			})

			Context("when the freshness bump fails", func() {
				BeforeEach(func() {
					storeAdapter.SetErrInjector = fakestoreadapter.NewFakeStoreAdapterErrorInjector("actual-fresh", errors.New("oops"))
				})

				It("logs about the failed freshness bump", func() {
					Ω(logger.LoggedSubjects).Should(ContainElement("Could not update actual freshness"))
				})
			})
		})

		Context("when the save fails", func() {
			BeforeEach(func() {
				storeAdapter.SetErrInjector = fakestoreadapter.NewFakeStoreAdapterErrorInjector(app.InstanceAtIndex(0).InstanceGuid, errors.New("oops"))
			})

			It("does not bump the freshness", func() {
				isFresh, _ := store.IsActualStateFresh(freshByTime)
				Ω(isFresh).Should(BeFalse())
			})

			It("logs about the failed save", func() {
				Ω(logger.LoggedSubjects).Should(ContainElement(ContainSubstring("Could not put heartbeat in store")))
			})
		})
	})

	Context("When it fails to parse the heartbeat message", func() {
		BeforeEach(func() {
			messageBus.Subscriptions["dea.heartbeat"][0].Callback(&yagnats.Message{
				Payload: "ß",
			})
		})

		It("Stores nothing in the store", func() {
			apps, _ := store.GetApps()
			Ω(apps).Should(BeEmpty())
		})

		It("does not bump the freshness", func() {
			isFresh, _ := store.IsActualStateFresh(freshByTime)
			Ω(isFresh).Should(BeFalse())
		})

		It("logs about the failed parse", func() {
			Ω(logger.LoggedSubjects).Should(ContainElement("Could not unmarshal heartbeat"))
		})
	})
})
