package vt

import (
	"encoding/base64"
	"strconv"
	"strings"
	"unicode/utf8"
)

// OSC 7501 program status limits, from the Program Status Protocol rev 0.4
// (https://www.superlogical.com/rex/docs/build/program-status). A report that
// breaks one is discarded whole, except the record cap, which evicts (store).
const (
	programStatusMaxRecords = 64 // the spec's floor; it allows up to 256
	// programStatusMaxReport keeps the whole sequence within the spec's 4096
	// bytes with either terminator: dispatch does not know which one ended it.
	programStatusMaxReport    = 4096 - len("\x1b]7501;\x1b\\")
	programStatusMaxKey       = 16
	programStatusMaxMsg       = 2732 // encoded; checked before decoding
	programStatusMaxMsgText   = 2048 // decoded
	programStatusMaxTitle     = 256
	programStatusMaxTitleText = 192
	programStatusMaxName      = 32 // an app value or one id segment
	programStatusMaxID        = 128
	programStatusMaxDepth     = 8
)

// programStatusReply answers the OSC 7501 feature query. It lists the states
// and kinds, though a bare "?" would declare the same set, so a program that
// echoes its input back cannot turn the reply into a second query and loop.
const programStatusReply = "\x1b]7501;?:states=idle,working,done,blocked,error:kinds=permission,question,auth\x1b\\"

// The OSC 7501 states a record can hold. ProgramStatus.State is one of them.
const (
	ProgramStateIdle    = "idle"
	ProgramStateWorking = "working"
	ProgramStateDone    = "done"
	ProgramStateBlocked = "blocked"
	ProgramStateError   = "error"
	programStateClear   = "clear" // removes records; never stored
)

// ProgramStatus is the terminal's OSC 7501 status, folded from its records
// (see Screen.ProgramStatus).
type ProgramStatus struct {
	State string // one of the ProgramState constants
	Kind  string // permission, question or auth with a blocked State, else ""
	// App is the folded record's app, or its nearest ancestor's; "" when none.
	App string
	// Progress is 0-100 with a working or blocked State; -1 when absent.
	Progress int
	// Seq is the Screen.ProgramReportSeq of the newest report that created a
	// record, changed one's state or kind, or cleared, and so changed which
	// record the fold shows or its State or Kind, a clear that leaves no
	// record included. A report that keeps a record's state and kind never
	// raises it, even when it hands the fold to another blocked record, and
	// nor does a prompt, an exit or a RIS. 0 before such a report; it never
	// falls.
	Seq uint64
}

// programRecord is one stored OSC 7501 record. touched is the number of the
// report that last wrote it, which orders eviction and the blocked fold.
type programRecord struct {
	state    string
	kind     string
	app      string
	touched  uint64
	progress int
}

func (r programRecord) unfinished() bool {
	return r.state == ProgramStateWorking || r.state == ProgramStateBlocked
}

// programEvent is one thing that changes the OSC 7501 record set.
type programEvent uint8

const (
	programEventReport programEvent = iota // an accepted report, a clear included
	programEventPrompt                     // OSC 133 A: a shell prompt begins
	programEventExit                       // the attached process exited
	programEventReset                      // RIS
)

// programChange is one programEvent with, for a report, its id, record and
// number.
type programChange struct {
	id    string
	rec   programRecord
	seq   uint64
	event programEvent
}

// programStatus owns a terminal's OSC 7501 record set; only apply changes it.
// A report stores its record (a new id past the cap first evicts the least
// recently reported one); a clear removes its id and subtree, the root id
// every record. A prompt or exit removes working and blocked records, and
// after exit every later change ends with that removal. RIS removes every
// record and ends reported; seq, occurrence and exited survive. A discarded report, DECSTR
// and an alternate-screen switch reach no event. docs/sessions.md "Record
// lifecycle" tabulates each cell.
type programStatus struct {
	records    map[string]programRecord // by id; "" is the root
	shown      programShown             // what the fold showed after the last change
	seq        uint64                   // the newest accepted report's number; never reset
	occurrence uint64                   // ProgramStatus.Seq
	reported   bool                     // a report was accepted since the last RIS
	exited     bool
}

// programShown identifies what the fold shows: the record and its state and
// kind; ok is false with no record to show.
type programShown struct {
	id, state, kind string
	ok              bool
}

func (p *programStatus) apply(c *programChange) {
	changed := false // a report changed a record's state or kind, or cleared
	switch c.event {
	case programEventReport:
		p.seq, p.reported = c.seq, true
		if c.rec.state == programStateClear {
			p.clear(c.id)
			changed = true
		} else {
			changed = p.store(c.id, c.rec, c.seq)
		}
	case programEventPrompt:
		p.dropUnfinished()
	case programEventExit:
		p.exited = true
	case programEventReset:
		p.records, p.reported = nil, false
	}
	if p.exited {
		p.dropUnfinished()
	}
	id, rec, ok := p.fold()
	shown := programShown{id: id, state: rec.state, kind: rec.kind, ok: ok}
	// A rewrite that keeps state and kind, a progress update included, can hand
	// the fold to another blocked record without a new occurrence.
	if changed && shown != p.shown {
		p.occurrence = c.seq
	}
	p.shown = shown
}

// store writes rec under id as report seq and reports whether the id is new or
// its state or kind changed.
func (p *programStatus) store(id string, rec programRecord, seq uint64) bool {
	rec.touched = seq
	prev, exists := p.records[id]
	changed := !exists || prev.state != rec.state || prev.kind != rec.kind
	if !exists && len(p.records) >= programStatusMaxRecords {
		p.evict()
	}
	if p.records == nil {
		p.records = make(map[string]programRecord)
	}
	p.records[id] = rec
	return changed
}

func (p *programStatus) clear(id string) {
	if id == "" {
		p.records = nil
		return
	}
	delete(p.records, id)
	prefix := id + "/"
	for k := range p.records {
		if strings.HasPrefix(k, prefix) {
			delete(p.records, k)
		}
	}
}

func (p *programStatus) dropUnfinished() {
	for id, r := range p.records {
		if r.unfinished() {
			delete(p.records, id)
		}
	}
}

// evict removes the least recently reported record.
func (p *programStatus) evict() {
	var oldest string
	var oldestSeq uint64
	first := true
	for id, r := range p.records {
		if first || r.touched < oldestSeq {
			oldest, oldestSeq, first = id, r.touched, false
		}
	}
	delete(p.records, oldest)
}

// handleOsc7501 answers the OSC 7501 feature query or applies one report.
func (s *Screen) handleOsc7501(data string) {
	if data == "?" {
		s.response = append(s.response, programStatusReply...)
		return
	}
	id, rec, ok := parseProgramReport(data)
	if !ok {
		return
	}
	s.program.apply(&programChange{event: programEventReport, id: id, rec: rec, seq: s.nextStatusSeq()})
}

// ProgramExited records that the process attached to the terminal exited:
// its working and blocked OSC 7501 records are dropped, as the protocol
// requires, and so is every such record a later report writes.
func (s *Screen) ProgramExited() {
	s.program.apply(&programChange{event: programEventExit})
}

// ProgramReportSeq is the number of the newest accepted OSC 7501 report, a
// clear included, drawn from NotificationSeq's counter, so comparing the two
// tells which channel spoke last. It is never reset; 0 means no report since
// the screen was made.
func (s *Screen) ProgramReportSeq() uint64 { return s.program.seq }

// ProgramReported reports whether an OSC 7501 report was accepted since the
// screen was made or last hard-reset (RIS).
func (s *Screen) ProgramReported() bool { return s.program.reported }

// ProgramStatus folds the OSC 7501 records into one status: the root record's,
// except that a blocked record wins, because a worker waiting on the user is
// what a status indicator exists to show. A blocked root wins outright,
// otherwise the most recently reported blocked child. With no root record and
// nothing blocked it is idle. Callers serialize access as for every other
// Screen method.
func (s *Screen) ProgramStatus() ProgramStatus {
	id, rec, ok := s.program.fold()
	if !ok {
		return ProgramStatus{State: ProgramStateIdle, Progress: progressAbsent, Seq: s.program.occurrence}
	}
	return ProgramStatus{State: rec.state, Kind: rec.kind, App: s.programApp(id), Progress: rec.progress, Seq: s.program.occurrence}
}

func (p *programStatus) fold() (string, programRecord, bool) {
	records := p.records
	root, hasRoot := records[""]
	if hasRoot && root.state == ProgramStateBlocked {
		return "", root, true
	}
	var blockedID string
	var blocked programRecord
	found := false
	for id, r := range records {
		if r.state == ProgramStateBlocked && (!found || r.touched > blocked.touched) {
			blockedID, blocked, found = id, r, true
		}
	}
	if found {
		return blockedID, blocked, true
	}
	return "", root, hasRoot
}

// programApp returns the app of record id, else of its nearest ancestor that
// has one, the inheritance the spec defines for a record without app.
func (s *Screen) programApp(id string) string {
	for {
		if r, ok := s.program.records[id]; ok && r.app != "" {
			return r.app
		}
		if id == "" {
			return ""
		}
		i := strings.LastIndexByte(id, '/')
		id = id[:max(i, 0)]
	}
}

// programFields holds the last value of each stored key an OSC 7501 report
// carries.
type programFields struct {
	state, id, kind, progress, app string
	hasID                          bool
}

// parseProgramReport validates an OSC 7501 report body and returns the record
// it addresses ("" for the root) and the record it writes; ok is false when
// the report must be ignored whole. Each pair takes the first rev 0.4 rule it
// meets, in this order: a key over 16 bytes discards the report; a pair with no
// "=" is skipped; a value over its key's limit discards; an id, in the value
// set or not, is the report's id and record judges the last one; any other
// value outside the value set is skipped; a msg or title that is not base64 of
// control-free text within its cap discards; an unknown key is skipped.
func parseProgramReport(data string) (id string, rec programRecord, ok bool) {
	if len(data) > programStatusMaxReport {
		return "", programRecord{}, false
	}
	var f programFields
	for pair := range strings.SplitSeq(data, ":") {
		key, value, hasValue := strings.Cut(pair, "=")
		key, value = strings.Trim(key, " \t"), strings.Trim(value, " \t")
		switch {
		case len(key) > programStatusMaxKey:
			return "", programRecord{}, false
		case !hasValue:
		case overProgramLimit(key, value):
			return "", programRecord{}, false
		case key == "id": // ahead of the value-set skip: a malformed id must not fall back to the root
			f.id, f.hasID = value, true
		case !isProgramValue(value):
		case !isProgramText(key, value):
			return "", programRecord{}, false
		default:
			f.set(key, value)
		}
	}
	return f.record()
}

// set stores value under a key the record keeps, a repeat replacing the
// earlier value, and ignores any other key.
func (f *programFields) set(key, value string) {
	switch key {
	case "state":
		f.state = value
	case "kind":
		f.kind = value
	case "progress":
		f.progress = value
	case "app":
		f.app = value
	}
}

func (f *programFields) record() (string, programRecord, bool) {
	if f.hasID && !isProgramID(f.id) {
		return "", programRecord{}, false
	}
	rec := programRecord{state: f.state, progress: progressAbsent}
	switch f.state {
	case ProgramStateIdle, ProgramStateDone, ProgramStateError, programStateClear:
	case ProgramStateWorking, ProgramStateBlocked:
		rec.progress = programProgress(f.progress)
	default:
		return "", programRecord{}, false // no state, or one this revision does not define
	}
	if f.state == ProgramStateBlocked {
		switch f.kind {
		case "permission", "question", "auth":
			rec.kind = f.kind
		}
	}
	if isProgramName(f.app) {
		rec.app = f.app
	}
	return f.id, rec, true
}

// programProgress reads a progress value: an integer 0-100, else absent.
func programProgress(v string) int {
	if v == "" || len(v) > 3 || !isAllDigits(v) {
		return progressAbsent
	}
	n, err := strconv.Atoi(v)
	if err != nil || n > progressPercentMax {
		return progressAbsent
	}
	return n
}

// overProgramLimit reports whether a pair's value breaks its key's size limit:
// app 32 bytes; id 128 bytes, 8 levels and 32 bytes a segment; msg and title
// their encoded caps.
func overProgramLimit(key, v string) bool {
	switch key {
	case "app":
		return len(v) > programStatusMaxName
	case "id":
		if len(v) > programStatusMaxID || strings.Count(v, "/") >= programStatusMaxDepth {
			return true
		}
		for seg := range strings.SplitSeq(v, "/") {
			if len(seg) > programStatusMaxName {
				return true
			}
		}
	case "msg":
		return len(v) > programStatusMaxMsg
	case "title":
		return len(v) > programStatusMaxTitle
	}
	return false
}

// isProgramText reports whether a msg or title value is empty, or standard
// base64 (padding optional) decoding to UTF-8 within the key's decoded cap
// with no control character; any other key passes. v is in the value set, so
// it holds none of the CR and LF the decoder would skip.
func isProgramText(key, v string) bool {
	maxDecoded := programStatusMaxMsgText
	switch key {
	case "msg":
	case "title":
		maxDecoded = programStatusMaxTitleText
	default:
		return true
	}
	if v == "" {
		return true
	}
	enc := base64.RawStdEncoding
	if strings.HasSuffix(v, "=") {
		enc = base64.StdEncoding
	}
	b, err := enc.DecodeString(v)
	if err != nil || len(b) > maxDecoded || !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

// isProgramID reports whether v matches the id grammar, "/"-separated names;
// overProgramLimit owns its lengths.
func isProgramID(v string) bool {
	for seg := range strings.SplitSeq(v, "/") {
		if !isProgramName(seg) {
			return false
		}
	}
	return true
}

// isProgramName reports whether v is one or more bytes of [A-Za-z0-9_.+-], the
// alphabet of an app value and of an id segment; overProgramLimit caps both at
// 32 bytes.
func isProgramName(v string) bool {
	if v == "" {
		return false
	}
	for i := range len(v) {
		if c := v[i]; !isAlnum(c) && c != '_' && c != '.' && c != '+' && c != '-' {
			return false
		}
	}
	return true
}

// isProgramValue reports whether every byte of v is in [A-Za-z0-9_.,+/=-].
func isProgramValue(v string) bool {
	for i := range len(v) {
		switch c := v[i]; {
		case isAlnum(c), c == '_', c == '.', c == ',', c == '+', c == '/', c == '=', c == '-':
		default:
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
