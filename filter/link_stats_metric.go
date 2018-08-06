package filter

import (
	"reflect"
	"strings"
	"time"

	"github.com/golang-collections/collections/stack"
	"github.com/golang/glog"
)

type LinkStatsMetricFilter struct {
	BaseFilter

	config        map[interface{}]interface{}
	timestamp     string
	batchWindow   int64
	reserveWindow int64
	overwrite     bool

	fields            []string
	fieldsWithoutLast []string
	lastField         string
	fieldsLength      int

	metric       map[int64]interface{}
	metricToEmit map[int64]interface{}
}

func NewLinkStatsMetricFilter(config map[interface{}]interface{}) *LinkStatsMetricFilter {
	baseFilter := NewBaseFilter(config)
	plugin := &LinkStatsMetricFilter{
		BaseFilter:   baseFilter,
		config:       config,
		overwrite:    true,
		metric:       make(map[int64]interface{}),
		metricToEmit: make(map[int64]interface{}),
	}

	if overwrite, ok := config["overwrite"]; ok {
		plugin.overwrite = overwrite.(bool)
	}

	if fieldsLink, ok := config["fieldsLink"]; ok {
		plugin.fields = strings.Split(fieldsLink.(string), "->")
		plugin.fieldsLength = len(plugin.fields)
		plugin.fieldsWithoutLast = plugin.fields[:plugin.fieldsLength-1]
		plugin.lastField = plugin.fields[plugin.fieldsLength-1]
	} else {
		glog.Fatal("fieldsLink must be set in linkstatmetric filter plugin")
	}

	if timestamp, ok := config["timestamp"]; ok {
		plugin.timestamp = timestamp.(string)
	} else {
		plugin.timestamp = "@timestamp"
	}

	if batchWindow, ok := config["batchWindow"]; ok {
		plugin.batchWindow = int64(batchWindow.(int))
	} else {
		glog.Fatal("batchWindow must be set in linkstatmetric filter plugin")
	}

	if reserveWindow, ok := config["reserveWindow"]; ok {
		plugin.reserveWindow = int64(reserveWindow.(int))
	} else {
		glog.Fatal("reserveWindow must be set in linkstatmetric filter plugin")
	}

	ticker := time.NewTicker(time.Second * time.Duration(plugin.batchWindow))
	go func() {
		for range ticker.C {
			if len(plugin.metric) > 0 && len(plugin.metricToEmit) == 0 {
				plugin.metricToEmit = plugin.metric
				plugin.metric = make(map[int64]interface{})
			}
		}
	}()
	return plugin
}

func (plugin *LinkStatsMetricFilter) updateMetric(event map[string]interface{}) {
	var value float32
	fieldValueI := event[plugin.lastField]
	if fieldValueI == nil {
		return
	}
	value = fieldValueI.(float32)

	var timestamp int64
	if v, ok := event[plugin.timestamp]; ok {
		if reflect.TypeOf(v).String() != "time.Time" {
			glog.V(10).Infof("timestamp must be time.Time, but it's %s", reflect.TypeOf(v).String())
			return
		}
		timestamp = v.(time.Time).Unix()
	} else {
		glog.V(10).Infof("not timestamp in event. %s", event)
		return
	}

	diff := time.Now().Unix() - timestamp
	if diff > plugin.reserveWindow || diff < 0 {
		return
	}

	timestamp -= timestamp % plugin.batchWindow
	var set map[string]interface{} = nil
	if v, ok := plugin.metric[timestamp]; ok {
		set = v.(map[string]interface{})
	} else {
		set = make(map[string]interface{})
		plugin.metric[timestamp] = set
	}

	var fieldValue string

	for _, field := range plugin.fieldsWithoutLast {
		fieldValueI := event[field]
		if fieldValueI == nil {
			return
		}
		fieldValue = fieldValueI.(string)
		if v, ok := set[fieldValue]; ok {
			set = v.(map[string]interface{})
		} else {
			set[fieldValue] = make(map[string]interface{})
			set = set[fieldValue].(map[string]interface{})
		}
	}

	if statsI, ok := set[plugin.lastField]; ok {
		stats := statsI.(map[string]float32)
		stats["count"] = 1 + stats["count"]
		stats["sum"] = value + stats["sum"]
	} else {
		stats := make(map[string]float32)
		stats["count"] = 1
		stats["sum"] = value
		set[plugin.lastField] = stats
	}
}

func (plugin *LinkStatsMetricFilter) Process(event map[string]interface{}) (map[string]interface{}, bool) {
	plugin.updateMetric(event)
	return event, false
}

func (plugin *LinkStatsMetricFilter) metricToEvents(metrics map[string]interface{}, level int) []map[string]interface{} {
	var (
		fieldName string                   = plugin.fields[level]
		events    []map[string]interface{} = make([]map[string]interface{}, 0)
	)

	if level == plugin.fieldsLength-1 {
		for fieldValue, statsI := range metrics {
			stats := statsI.(map[string]float32)
			event := map[string]interface{}{fieldName: fieldValue}
			event["count"] = int(stats["count"])
			event["sum"] = stats["sum"]
			event["mean"] = stats["sum"] / stats["count"]
			events = append(events, event)
		}
		return events
	}

	for fieldValue, nextLevelMetrics := range metrics {
		for _, e := range plugin.metricToEvents(nextLevelMetrics.(map[string]interface{}), level+1) {
			event := make(map[string]interface{})
			event[fieldName] = fieldValue
			for k, v := range e {
				event[k] = v
			}
			events = append(events, event)
		}
	}

	return events
}

func (plugin *LinkStatsMetricFilter) EmitExtraEvents(sTo *stack.Stack) {
	if len(plugin.metricToEmit) == 0 {
		return
	}
	var event map[string]interface{}
	for timestamp, metrics := range plugin.metricToEmit {
		for _, event = range plugin.metricToEvents(metrics.(map[string]interface{}), 0) {
			event[plugin.timestamp] = time.Unix(timestamp, 0)
			event = plugin.PostProcess(event, true)
			sTo.Push(event)
		}
	}
	plugin.metricToEmit = make(map[int64]interface{})
	return
}