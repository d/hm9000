package hm

import (
	"github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/hm9000/config"
	"github.com/cloudfoundry/hm9000/helpers/logger"
	"github.com/cloudfoundry/hm9000/helpers/metricsaccountant"
	"github.com/cloudfoundry/hm9000/metricsserver"
	"github.com/cloudfoundry/loggregatorlib/cfcomponent/registrars/collectorregistrar"
)

func ServeMetrics(steno *gosteno.Logger, l logger.Logger, conf config.Config) {
	store := connectToStore(l, conf)
	cfMessageBus := connectToCFMessageBus(l, conf)

	collectorRegistrar := collectorregistrar.NewCollectorRegistrar(cfMessageBus, steno)

	metricsServer := metricsserver.New(
		collectorRegistrar,
		steno,
		metricsaccountant.New(store),
		l,
		store,
		buildTimeProvider(l),
		conf,
	)

	err := metricsServer.Start()
	if err != nil {
		l.Error("Failed to serve metrics", err)
	}
	l.Info("Serving Metrics")
	select {}
}
