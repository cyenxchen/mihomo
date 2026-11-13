package netenv

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/metacubex/mihomo/log"
)

type (
	Slot struct {
		Snapshot Snapshot  `json:"snapshot"`
		Source   string    `json:"source"`
		Time     time.Time `json:"time"`
		Valid    bool      `json:"valid"`
	}
	View struct {
		Snapshot
		Report      Slot              `json:"report"`
		Detect      Slot              `json:"detect"`
		Generation  uint64            `json:"generation"`
		Sources     map[string]string `json:"sources"`
		Unavailable []string          `json:"unavailable"`
	}
	Detector func(context.Context) (Snapshot, error)
	Store    struct {
		mu               sync.RWMutex
		now              func() time.Time
		view             View
		detector         Detector
		detectorEpoch    uint64
		detectionRequest uint64
		detectionApplied uint64
		binding          Snapshot
		bound            bool
		reportID         uint64
		subscribers      map[uint64]func(Snapshot)
		nextSubscriber   uint64
	}
)

const reportBindingWindow = 3 * time.Second

var Default = NewStore(time.Now)

func NewStore(now func() time.Time) *Store {
	return &Store{now: now, view: View{Snapshot: Snapshot{State: Unknown}}, subscribers: make(map[uint64]func(Snapshot))}
}
func (s *Store) Current() Snapshot { s.mu.RLock(); defer s.mu.RUnlock(); return s.view.Snapshot }
func (s *Store) View() View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.view
	v.Sources = make(map[string]string, len(s.view.Sources))
	for k, x := range s.view.Sources {
		v.Sources[k] = x
	}
	v.Unavailable = append([]string(nil), s.view.Unavailable...)
	return v
}

func identityChanged(a, b Snapshot) bool {
	if a.State != b.State {
		return true
	}
	av, bv := a.Fields.values(), b.Fields.values()
	for _, i := range []int{0, 1, 4, 5, 6} {
		if av[i] != "" && bv[i] != "" && av[i] != bv[i] {
			return true
		}
	}
	return false
}

func (s *Store) mergeLocked() []func(Snapshot) {
	if !s.view.Report.Valid && !s.view.Detect.Valid {
		return nil
	}
	next := Snapshot{State: Online}
	sources := map[string]string{}
	if s.view.Detect.Valid {
		next = s.view.Detect.Snapshot
		for i, v := range next.Fields.values() {
			if v != "" {
				sources[fieldNames[i]] = "detect"
			}
		}
	}
	if s.view.Report.Valid {
		r := s.view.Report.Snapshot
		if r.State == None {
			next = r
			sources = map[string]string{"type": "report"}
		} else {
			// An online report describes a network, even if local detection saw none.
			next.State = Online
			ptrs := next.Fields.pointers()
			for i, v := range r.Fields.values() {
				if v != "" {
					*ptrs[i] = v
					sources[fieldNames[i]] = "report"
				}
			}
		}
	}
	missing := []string{}
	for i, v := range next.Fields.values() {
		if v == "" {
			missing = append(missing, fieldNames[i])
		}
	}
	changed := next != s.view.Snapshot
	s.view.Snapshot = next
	s.view.Sources = sources
	s.view.Unavailable = missing
	if !changed {
		return nil
	}
	s.view.Generation++
	callbacks := make([]func(Snapshot), 0, len(s.subscribers))
	for _, fn := range s.subscribers {
		callbacks = append(callbacks, fn)
	}
	return callbacks
}

func (s *Store) notify(callbacks []func(Snapshot)) {
	for _, fn := range callbacks {
		fn(s.Current())
	}
}

func (s *Store) applyDetectionLocked(value Snapshot) {
	s.view.Detect = Slot{Snapshot: value, Source: "detect", Time: s.now(), Valid: true}
	if !s.view.Report.Valid {
		return
	}
	if !s.bound {
		if s.now().Sub(s.view.Report.Time) > reportBindingWindow {
			s.view.Report.Valid = false
			return
		}
		s.binding = value
		s.bound = true
	}
	if identityChanged(s.binding, value) || (s.view.Report.Snapshot.Interface != "" && value.Interface != "" && s.view.Report.Snapshot.Interface != value.Interface) {
		s.view.Report.Valid = false
	}
}

func (s *Store) Report(ctx context.Context, f Fields, source string) error {
	raw := map[string]any{}
	for i, v := range f.values() {
		if v != "" {
			raw[fieldNames[i]] = v
		}
	}
	f, err := ParseFields(raw, true)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.reportID++
	id := s.reportID
	receivedAt := s.now()
	fn, epoch := s.detector, s.detectorEpoch
	s.detectionRequest++
	request := s.detectionRequest
	s.mu.Unlock()
	var detected Snapshot
	if fn != nil {
		detected, err = fn(ctx)
	}
	s.mu.Lock()
	if id != s.reportID {
		s.mu.Unlock()
		return nil
	}
	usable := fn != nil && err == nil && epoch == s.detectorEpoch
	if usable {
		if request < s.detectionApplied {
			detected = s.view.Detect.Snapshot
		} else {
			s.detectionApplied = request
			s.applyDetectionLocked(detected)
		}
	}
	snapshot := Snapshot{State: Online, Fields: f}
	if f.Type == "none" {
		snapshot = Snapshot{State: None}
	}
	s.view.Report = Slot{Snapshot: snapshot, Source: source, Time: receivedAt, Valid: true}
	s.bound = false
	if !usable && s.view.Detect.Valid {
		detected = s.view.Detect.Snapshot
		usable = true
	}
	if usable {
		s.binding = detected
		s.bound = true
		if f.Interface != "" && detected.Interface != "" && f.Interface != detected.Interface {
			s.view.Report.Valid = false
			log.Warnln("[Subnet] ignored report interface %s: physical default is %s", f.Interface, detected.Interface)
		}
	}
	callbacks := s.mergeLocked()
	s.mu.Unlock()
	s.notify(callbacks)
	return nil
}

func (s *Store) ClearReport() {
	s.mu.Lock()
	s.reportID++
	s.view.Report.Valid = false
	s.bound = false
	callbacks := s.mergeLocked()
	s.mu.Unlock()
	s.notify(callbacks)
}

func (s *Store) StopDetection() {
	s.mu.Lock()
	s.detector = nil
	s.detectorEpoch++
	s.view.Detect.Valid = false
	callbacks := s.mergeLocked()
	s.mu.Unlock()
	s.notify(callbacks)
}

func (s *Store) SetDetector(fn Detector) {
	s.mu.Lock()
	s.detector = fn
	s.detectorEpoch++
	s.mu.Unlock()
}

func (s *Store) Detect(ctx context.Context) error {
	s.mu.Lock()
	fn, epoch := s.detector, s.detectorEpoch
	s.detectionRequest++
	request := s.detectionRequest
	s.mu.Unlock()
	if fn == nil {
		return errors.New("network detection unavailable")
	}
	value, err := fn(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if epoch != s.detectorEpoch || request < s.detectionApplied {
		s.mu.Unlock()
		return nil
	}
	s.detectionApplied = request
	s.applyDetectionLocked(value)
	callbacks := s.mergeLocked()
	s.mu.Unlock()
	s.notify(callbacks)
	return nil
}

func (s *Store) Subscribe(fn func(Snapshot)) func() {
	s.mu.Lock()
	s.nextSubscriber++
	id := s.nextSubscriber
	s.subscribers[id] = fn
	s.mu.Unlock()
	return func() { s.mu.Lock(); delete(s.subscribers, id); s.mu.Unlock() }
}
