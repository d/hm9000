package store_test

import (
	"github.com/cloudfoundry/hm9000/config"
	. "github.com/cloudfoundry/hm9000/store"
	"github.com/cloudfoundry/hm9000/storeadapter"
	"github.com/cloudfoundry/hm9000/testhelpers/fakelogger"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Metrics", func() {
	var (
		store       Store
		etcdAdapter storeadapter.StoreAdapter
		conf        config.Config
	)

	conf, _ = config.DefaultConfig()

	BeforeEach(func() {
		etcdAdapter = storeadapter.NewETCDStoreAdapter(etcdRunner.NodeURLS(), conf.StoreMaxConcurrentRequests)
		err := etcdAdapter.Connect()
		Ω(err).ShouldNot(HaveOccured())

		store = NewStore(conf, etcdAdapter, fakelogger.NewFakeLogger())
	})

	Describe("Getting and setting a metric", func() {
		BeforeEach(func() {
			err := store.SaveMetric("sprockets", 17)
			Ω(err).ShouldNot(HaveOccured())
		})

		It("should store the metric under /metrics", func() {
			_, err := etcdAdapter.Get("/metrics/sprockets")
			Ω(err).ShouldNot(HaveOccured())
		})

		Context("when the metric is present", func() {
			It("should return the stored value and no error", func() {
				value, err := store.GetMetric("sprockets")
				Ω(err).ShouldNot(HaveOccured())
				Ω(value).Should(Equal(17))
			})

			Context("and it is overwritten", func() {
				BeforeEach(func() {
					err := store.SaveMetric("sprockets", 23)
					Ω(err).ShouldNot(HaveOccured())
				})

				It("should return the new value", func() {
					value, err := store.GetMetric("sprockets")
					Ω(err).ShouldNot(HaveOccured())
					Ω(value).Should(Equal(23))
				})
			})
		})

		Context("when the metric is not present", func() {
			It("should return -1 and an error", func() {
				value, err := store.GetMetric("nonexistent")
				Ω(err).Should(Equal(storeadapter.ErrorKeyNotFound))
				Ω(value).Should(Equal(-1))
			})
		})
	})
})
