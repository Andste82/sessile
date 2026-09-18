package tasks

import "encoding/json"

// List returns userID's tasks.
func (s *Service) List(userID string) ([]Task, error) {
	rows, err := s.DB.ListTasks(userID)
	if err != nil {
		return nil, err
	}
	out := []Task{}
	for _, row := range rows {
		var spec Spec
		if err := json.Unmarshal([]byte(row.SpecJSON), &spec); err != nil {
			continue
		}
		out = append(out, Task{ID: row.ID, SessionID: row.SessionID, HostID: row.HostID, Dir: row.Dir,
			Spec: spec, Summary: row.Summary, Created: row.Created})
	}
	return out, nil
}
