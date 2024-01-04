package cache

import (
	"fmt"

	"github.com/longhorn/longhorn-manager/engineapi"
)

type MetricsCache struct {
	data map[string]uint64
}

func NewMetricsCache() *MetricsCache {
	c := &MetricsCache{}
	return c
}

func (m *MetricsCache) Put(key string, value uint64) {
	m.data[key] = value
}

func (m *MetricsCache) PutVolumeMetrics(name string, value *engineapi.Metrics) {
	m.data[m.Key("volume", name, "read_throughput")] = value.ReadThroughput
	m.data[m.Key("volume", name, "write_throughput")] = value.WriteThroughput
	m.data[m.Key("volume", name, "read_latency")] = value.ReadLatency
	m.data[m.Key("volume", name, "write_latency")] = value.WriteLatency
	m.data[m.Key("volume", name, "read_iops")] = value.ReadIOPS
	m.data[m.Key("volume", name, "write_iops")] = value.WriteIOPS
}

func (m *MetricsCache) Get(key string) uint64 {
	d, ok := m.data[key]
	if ok {
		return d
	} else {
		return 0
	}
}

func (m *MetricsCache) Key(kind, name, info string) string {
	return fmt.Sprintf("%v/%v/%v", kind, name, info)
}
