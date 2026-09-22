package cronschedule

import "time"

// Occurrence records the timing accepted by the scheduler. Applications use this
// immutable context when rendering their inputs, including on delayed retries.
type Occurrence struct {
	Name        string     `json:"name"`
	DueAt       time.Time  `json:"due_at"`
	FiredAt     time.Time  `json:"fired_at"`
	LastFiredAt *time.Time `json:"last_fired_at,omitempty"`
	Timezone    string     `json:"timezone"`
}

func (o Occurrence) MessageData() (map[string]any, error) {
	return OccurrenceMessageData(o.Name, o.FiredAt, o.LastFiredAt, o.DueAt, o.Timezone)
}
