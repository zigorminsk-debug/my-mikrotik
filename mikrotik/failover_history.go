package mikrotik

import (
	"regexp"
	"strings"
)

// Log messages are not returned verbatim: even a matching prefix can contain
// secrets or arbitrary user text. Recognise only known, anchored patterns and
// emit fixed labels plus tightly constrained numeric/boolean captures.
const maxFailoverEvents = 100

var failoverSwitch = regexp.MustCompile(`^mti-failover: backup=(true|false), приоритет основного канала ([0-9]{1,3})$`)
var failoverState = regexp.MustCompile(`^mti-failover: состояние — ответов ([0-3]) из 3, backup=(true|false), приоритет=([0-9]{1,3}|-), активный=[^,\r\n]{1,128}, маршрутов=([0-9]{1,6}), из них динамических ([0-9]{1,6})$`)
var failoverNoResponse = regexp.MustCompile(`^mti-failover: нет ответа от [^\r\n]{1,64} \(0 из 3, тип=[a-zA-Z0-9_-]{1,32}\), отсчёт 30 c$`)
var failoverResponse = regexp.MustCompile(`^mti-failover: ответ от [^\r\n]{1,64} получен \(([1-3]) из 3\), отсчёт 60 c$`)
var failoverFailed = regexp.MustCompile(`^mti-failover: переключение не удалось: приоритет остался (-?[0-9]{1,3}) вместо ([0-9]{1,3})$`)
var failoverRetry = regexp.MustCompile(`^mti-failover: приоритет основного канала остался (-?[0-9]{1,3}) вместо ([0-9]{1,3}), повторяю$`)
var failoverPriority = regexp.MustCompile(`^mti-failover: (создан маршрут основного канала, приоритет |маршрут основного канала обновлён, приоритет )([0-9]{1,3})$`)

// RouterOS log formats are local router time, sometimes without a date.
var failoverTime = regexp.MustCompile(`^(?:[0-9]{2}:[0-9]{2}:[0-9]{2}|[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}|[a-zA-Z]{3}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}|[a-zA-Z]{3}/[0-9]{2}/[0-9]{4} [0-9]{2}:[0-9]{2}:[0-9]{2})$`)

func failoverEvent(rec map[string]string) map[string]string {
	msg := rec["message"]
	row := map[string]string{}
	switch {
	case failoverSwitch.MatchString(msg):
		m := failoverSwitch.FindStringSubmatch(msg)
		row["event"] = "Контроллер сообщил о переключении"
		row["backup"] = m[1]
		row["distance"] = m[2]
	case failoverState.MatchString(msg):
		m := failoverState.FindStringSubmatch(msg)
		row["event"] = "Периодическое состояние контроллера"
		row["replies"] = m[1] + "/3"
		row["backup"] = m[2]
		row["distance"] = m[3]
		row["routes"] = m[4]
		row["dynamic-routes"] = m[5]
	case failoverNoResponse.MatchString(msg):
		row["event"] = "Нет ответа контрольной пробы; начат отсчёт"
	case failoverResponse.MatchString(msg):
		m := failoverResponse.FindStringSubmatch(msg)
		row["event"] = "Ответ контрольной пробы; начат отсчёт"
		row["replies"] = m[1] + "/3"
	case failoverFailed.MatchString(msg):
		m := failoverFailed.FindStringSubmatch(msg)
		row["event"] = "Контроллер сообщил, что переключение не удалось"
		row["distance"] = m[1]
		row["expected-distance"] = m[2]
	case failoverRetry.MatchString(msg):
		m := failoverRetry.FindStringSubmatch(msg)
		row["event"] = "Контроллер сообщил о повторной попытке"
		row["distance"] = m[1]
		row["expected-distance"] = m[2]
	case failoverPriority.MatchString(msg):
		m := failoverPriority.FindStringSubmatch(msg)
		if strings.HasPrefix(m[1], "создан") {
			row["event"] = "Контроллер сообщил о создании маршрута"
		} else {
			row["event"] = "Контроллер сообщил об обновлении маршрута"
		}
		row["distance"] = m[2]
	case msg == "mti-failover: нет шлюза основного канала — проба пропущена":
		row["event"] = "Нет шлюза основного канала; проба пропущена"
	case msg == "mti-failover: не удалось создать маршрут основного канала":
		row["event"] = "Не удалось создать маршрут основного канала"
	case msg == "mti-failover: не удалось обновить маршрут основного канала":
		row["event"] = "Не удалось обновить маршрут основного канала"
	case msg == "mti-failover: не удалось изменить приоритет основного канала":
		row["event"] = "Не удалось изменить приоритет основного канала"
	case msg == "mti-failover: не удалось создать управляемый маршрут — основной канал оставляет свой":
		row["event"] = "Не удалось создать управляемый маршрут"
	default:
		return nil
	}
	if t := rec["time"]; failoverTime.MatchString(t) {
		row["time"] = t
	}
	return row
}

func historySection(records []map[string]string) SnapshotSection {
	s := SnapshotSection{Status: "ok", Rows: []map[string]string{}}
	// RouterOS normally returns logs oldest-first. Preserve server ordering; do not
	// infer missing dates or sort by a time string with no timezone or date.
	for _, record := range records {
		if row := failoverEvent(record); row != nil {
			s.Rows = append(s.Rows, row)
		}
	}
	if len(s.Rows) > maxFailoverEvents {
		s.Truncated = true
		s.Rows = s.Rows[len(s.Rows)-maxFailoverEvents:]
	}
	return s
}
