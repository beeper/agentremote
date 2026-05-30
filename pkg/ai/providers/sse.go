package providers

import (
	"bufio"
	"io"
	"strings"
)

type serverSentEvent struct {
	Event string
	Data  string
	Raw   []string
}

func iterateSSE(reader io.Reader, handle func(serverSentEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	eventType := ""
	data := []string{}
	raw := []string{}
	flush := func() error {
		if eventType == "" && len(data) == 0 {
			return nil
		}
		event := serverSentEvent{Event: eventType, Data: strings.Join(data, "\n"), Raw: append([]string{}, raw...)}
		eventType = ""
		data = nil
		raw = nil
		return handle(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		raw = append(raw, line)
		if strings.HasPrefix(line, ":") {
			continue
		}
		field := line
		value := ""
		if index := strings.Index(line, ":"); index >= 0 {
			field = line[:index]
			value = line[index+1:]
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			eventType = value
		case "data":
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}
