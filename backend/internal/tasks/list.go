package tasks

// List returns userID's tasks, newest first.
func (s *Service) List(userID string) ([]Task, error) {
	rows, err := s.DB.ListTasks(userID)
	if err != nil {
		return nil, err
	}
	out := []Task{}
	for _, row := range rows {
		t, err := fromRow(row)
		if err != nil {
			continue // one task whose spec no longer decodes shouldn't hide the rest
		}
		out = append(out, t)
	}
	return out, nil
}
