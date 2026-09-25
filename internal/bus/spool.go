package bus

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

// Spool is a carrier that keeps every delivered message in an append-only
// JSONL file. The file is the transport, not a log: a recipient's messages
// are read back from it, so what an agent receives is exactly what the file
// holds. One line per delivery, a duplicate delivery is two lines.
type Spool struct {
	f      *os.File
	size   int64            // bytes written so far
	cursor map[string]int64 // per recipient, the offset up to which the file has been read
	unread map[string]int
}

type spoolLine struct {
	ID   int64  `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
}

// OpenSpool creates or truncates the file at path.
func OpenSpool(path string) (*Spool, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &Spool{f: f, cursor: map[string]int64{}, unread: map[string]int{}}, nil
}

// Deliver appends one message.
func (s *Spool) Deliver(m Message) error {
	b, err := json.Marshal(spoolLine(m))
	if err != nil {
		return err
	}
	b = append(b, '\n')
	n, err := s.f.WriteAt(b, s.size)
	s.size += int64(n)
	if err != nil {
		return fmt.Errorf("spool: %w", err)
	}
	s.unread[m.To]++
	return nil
}

// Holds reports undelivered messages for an agent without reading the file.
func (s *Spool) Holds(to string) bool { return s.unread[to] > 0 }

// Collect reads the file from the agent's cursor to the end and returns the
// lines addressed to it.
func (s *Spool) Collect(to string) ([]Message, error) {
	if s.unread[to] == 0 {
		return nil, nil
	}
	start := s.cursor[to]
	sc := bufio.NewScanner(io.NewSectionReader(s.f, start, s.size-start))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var out []Message
	for sc.Scan() {
		line := sc.Bytes()
		if !gjson.ValidBytes(line) {
			return out, fmt.Errorf("spool: corrupt line after offset %d", start)
		}
		r := gjson.ParseBytes(line)
		if r.Get("to").String() != to {
			continue
		}
		out = append(out, Message{ID: r.Get("id").Int(), From: r.Get("from").String(), To: to, Text: r.Get("text").String()})
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("spool: %w", err)
	}
	if len(out) != s.unread[to] {
		return out, fmt.Errorf("spool: %s expected %d messages, the file holds %d", to, s.unread[to], len(out))
	}
	s.cursor[to] = s.size
	s.unread[to] = 0
	return out, nil
}

// Close closes the file. The contents stay for inspection.
func (s *Spool) Close() error {
	if s.f == nil {
		return errors.New("spool: already closed")
	}
	err := s.f.Close()
	s.f = nil
	return err
}

var _ Carrier = (*Spool)(nil)
