package session

import "testing"

// size reads the geometry the session settled on.
func size(t *testing.T, s *Session) (uint16, uint16) {
	t.Helper()
	i := s.Info()
	return i.Rows, i.Cols
}

// The point of the feature: one PTY serves every attached client, so it has to
// fit in the smallest window, or the others render a width the program is not
// writing for.
func TestResizeTakesTheSmallestClient(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.Create("resize", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := liveSession(t, mgr, info.ID)

	desktop := &recordingClient{id: "desktop"}
	phone := &recordingClient{id: "phone"}
	for _, c := range []*recordingClient{desktop, phone} {
		if _, err := mgr.Attach(info.ID, c); err != nil {
			t.Fatalf("attach %s: %v", c.id, err)
		}
	}

	// One client, one answer: nothing else is attached with a size to respect.
	if err := mgr.Resize(info.ID, desktop, 50, 120); err != nil {
		t.Fatalf("resize desktop: %v", err)
	}
	if rows, cols := size(t, s); rows != 50 || cols != 120 {
		t.Fatalf("one client reporting 50x120: got %dx%d", rows, cols)
	}

	// The phone is narrower and shorter, so it decides both.
	if err := mgr.Resize(info.ID, phone, 20, 40); err != nil {
		t.Fatalf("resize phone: %v", err)
	}
	if rows, cols := size(t, s); rows != 20 || cols != 40 {
		t.Fatalf("phone attached: got %dx%d, want 20x40", rows, cols)
	}

	// The desktop reporting again must not take the session back off the phone:
	// last-one-wins is exactly what this replaces.
	if err := mgr.Resize(info.ID, desktop, 60, 200); err != nil {
		t.Fatalf("resize desktop again: %v", err)
	}
	if rows, cols := size(t, s); rows != 20 || cols != 40 {
		t.Fatalf("desktop resized under a phone: got %dx%d, want 20x40", rows, cols)
	}
}

// The axes are taken independently: a phone held upright is short and narrow,
// but a wide, shallow desktop window constrains only the rows, and the session
// has to fit inside both at once.
func TestResizeTakesEachAxisFromWhicheverClientIsSmaller(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.Create("axes", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := liveSession(t, mgr, info.ID)

	tall := &recordingClient{id: "tall-narrow"}
	wide := &recordingClient{id: "wide-short"}
	for _, c := range []*recordingClient{tall, wide} {
		if _, err := mgr.Attach(info.ID, c); err != nil {
			t.Fatalf("attach %s: %v", c.id, err)
		}
	}

	if err := mgr.Resize(info.ID, tall, 80, 40); err != nil {
		t.Fatalf("resize tall: %v", err)
	}
	if err := mgr.Resize(info.ID, wide, 15, 200); err != nil {
		t.Fatalf("resize wide: %v", err)
	}
	if rows, cols := size(t, s); rows != 15 || cols != 40 {
		t.Fatalf("got %dx%d, want 15 rows from the short one and 40 cols from the narrow one", rows, cols)
	}
}

// A client leaving gives its constraint up: the windows that remain should get
// their space back rather than staying squeezed by a phone that is gone.
func TestDetachReleasesTheSizeItHeld(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.Create("detach", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := liveSession(t, mgr, info.ID)

	desktop := &recordingClient{id: "desktop"}
	phone := &recordingClient{id: "phone"}
	for _, c := range []*recordingClient{desktop, phone} {
		if _, err := mgr.Attach(info.ID, c); err != nil {
			t.Fatalf("attach %s: %v", c.id, err)
		}
	}
	if err := mgr.Resize(info.ID, desktop, 50, 120); err != nil {
		t.Fatalf("resize desktop: %v", err)
	}
	if err := mgr.Resize(info.ID, phone, 20, 40); err != nil {
		t.Fatalf("resize phone: %v", err)
	}

	mgr.Detach(info.ID, phone)
	if rows, cols := size(t, s); rows != 50 || cols != 120 {
		t.Fatalf("after the phone left: got %dx%d, want the desktop's 50x120", rows, cols)
	}

	// The last client leaving takes no size with it: there is no window left to
	// fit, and the running program keeps the geometry it was last told about.
	mgr.Detach(info.ID, desktop)
	if rows, cols := size(t, s); rows != 50 || cols != 120 {
		t.Fatalf("after every client left: got %dx%d, want 50x120 kept", rows, cols)
	}
}

// A client that has attached but not yet said how big it is must not drag the
// session anywhere — least of all to a default nobody is displaying.
func TestAttachWithoutResizeDoesNotChangeTheSize(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.Create("silent", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := liveSession(t, mgr, info.ID)

	desktop := &recordingClient{id: "desktop"}
	if _, err := mgr.Attach(info.ID, desktop); err != nil {
		t.Fatalf("attach desktop: %v", err)
	}
	if err := mgr.Resize(info.ID, desktop, 50, 120); err != nil {
		t.Fatalf("resize desktop: %v", err)
	}

	if _, err := mgr.Attach(info.ID, &recordingClient{id: "quiet"}); err != nil {
		t.Fatalf("attach quiet: %v", err)
	}
	if rows, cols := size(t, s); rows != 50 || cols != 120 {
		t.Fatalf("a client that never reported a size changed it: got %dx%d", rows, cols)
	}
}

// A resize from a connection that is not attached has no window behind it.
func TestResizeFromADetachedClientIsIgnored(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.Create("stale", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := liveSession(t, mgr, info.ID)

	desktop := &recordingClient{id: "desktop"}
	if _, err := mgr.Attach(info.ID, desktop); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := mgr.Resize(info.ID, desktop, 50, 120); err != nil {
		t.Fatalf("resize: %v", err)
	}

	gone := &recordingClient{id: "gone"}
	if err := mgr.Resize(info.ID, gone, 5, 5); err != nil {
		t.Fatalf("resize from a detached client: %v", err)
	}
	if rows, cols := size(t, s); rows != 50 || cols != 120 {
		t.Fatalf("a detached client set the size: got %dx%d", rows, cols)
	}
}
