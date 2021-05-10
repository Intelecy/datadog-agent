//+build zlib

package serializer

import (
	"fmt"
	"time"

	"github.com/DataDog/datadog-agent/pkg/metrics"
	"github.com/DataDog/datadog-agent/pkg/serializer/stream"
)

func benchmarkJSONPayloadBuilderThroughput(points int, items int, tags int, runs int) { //nolint:unuse
	series := metrics.Series{}
	for i := 0; i < items; i++ {
		series = append(series, &metrics.Serie{
			Points: func() []metrics.Point {
				ps := make([]metrics.Point, points)
				for p := 0; p < points; p++ {
					ps[p] = metrics.Point{Ts: float64(p * i), Value: float64(p + i)}
				}
				return ps
			}(),
			MType:    metrics.APIGaugeType,
			Name:     "test.metrics",
			Interval: 15,
			Host:     "localHost",
			Tags: func() []string {
				ts := make([]string, tags)
				for t := 0; t < tags; t++ {
					ts[t] = fmt.Sprintf("tag%d:foobar", t)
				}
				return ts
			}(),
		})
	}
	json, _ := series.MarshalJSON()
	initialSize := len(json)
	metricsCount := len(series)

	payloadBuilder := stream.NewJSONPayloadBuilder(true)
	var totalTime time.Duration

	for i := 0; i < runs; i++ {
		start := time.Now()
		payloadBuilder.Build(series)
		totalTime += time.Since(start)
	}
	avgTime := int64(totalTime) / int64(runs)
	speed := float64(initialSize) / (float64(avgTime) / float64(time.Second))
	metricRate := int(float64(metricsCount) / (float64(avgTime) / float64(time.Second)))
	megabyte := float64(1024 * 1024)
	fmt.Printf("inputSize: %d bytes \t # of metrics: %d \t tags: %d \t points: %d \t avg duration: %s \t throughput: %f MB/sec \t metrics/sec: %d\n", initialSize, metricsCount, tags, points, fmt.Sprint(time.Duration(avgTime)), speed/megabyte, metricRate)
}

// Uncomment to run these benchmarks. These are non-standard benchmarks to collect custom metrics
// so they should not be run as part of normal CI.

// func TestJSONPayloadBuilderThroughputPoints(t *testing.T) {
// 	// # of points and items chosen to be approximately the same # of bytes per payload between tests
// 	benchmarkJSONPayloadBuilderThroughput(0, 21000, 1, 10)
// 	benchmarkJSONPayloadBuilderThroughput(1, 20000, 1, 10)
// 	benchmarkJSONPayloadBuilderThroughput(2, 20000, 1, 10)
// 	benchmarkJSONPayloadBuilderThroughput(5, 15000, 1, 10)
// 	benchmarkJSONPayloadBuilderThroughput(10, 10000, 1, 10)
// 	benchmarkJSONPayloadBuilderThroughput(100, 2000, 1, 10)
// 	benchmarkJSONPayloadBuilderThroughput(200, 1000, 1, 10)
// }

// func TestJSONPayloadBuilderThroughputTags(t *testing.T) {
// 	// # of points and items chosen to be approximately the same # of bytes per payload between tests
// 	benchmarkJSONPayloadBuilderThroughput(1, 21000, 1, 10)
// 	benchmarkJSONPayloadBuilderThroughput(1, 21000, 2, 10)
// 	benchmarkJSONPayloadBuilderThroughput(1, 19000, 5, 10)
// 	benchmarkJSONPayloadBuilderThroughput(1, 15000, 10, 10)
// 	benchmarkJSONPayloadBuilderThroughput(1, 2000, 100, 10)
// 	benchmarkJSONPayloadBuilderThroughput(1, 200, 1000, 10)
// 	benchmarkJSONPayloadBuilderThroughput(1, 20, 10000, 10)
// }

// func TestJSONPayloadBuilderThroughputHighRate(t *testing.T) {
// 	// warning - These tests are very slow
// 	benchmarkJSONPayloadBuilderThroughput(1, 1000000, 1, 1)
// 	benchmarkJSONPayloadBuilderThroughput(1, 1000000, 10, 1)
// 	benchmarkJSONPayloadBuilderThroughput(2, 1000000, 1, 1)
// 	benchmarkJSONPayloadBuilderThroughput(2, 1000000, 10, 1)
// 	benchmarkJSONPayloadBuilderThroughput(4, 500000, 1, 1)
// 	benchmarkJSONPayloadBuilderThroughput(4, 500000, 10, 1)
// }
