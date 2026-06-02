package aicommand

import "testing"

func TestRegistryParsesVisibleAndHiddenCommands(t *testing.T) {
	registry := NewRegistry([]string{"model", "approve", "abort"}, map[string]string{"stop": "abort"})
	tests := []struct {
		name   string
		parse  func(string) (Command, bool)
		body   string
		want   Command
		wantOK bool
	}{
		{
			name:   "visible slash",
			parse:  registry.ParseVisible,
			body:   "/model gpt-5",
			want:   Command{Name: "model", Arg: "gpt-5"},
			wantOK: true,
		},
		{
			name:   "hidden slash",
			parse:  registry.ParseHidden,
			body:   "/approve approval-1 approve",
			want:   Command{Name: "approve", Arg: "approval-1 approve"},
			wantOK: true,
		},
		{
			name:   "hidden ai prefix",
			parse:  registry.ParseHidden,
			body:   "!ai stop",
			want:   Command{Name: "abort"},
			wantOK: true,
		},
		{
			name:   "visible ignores ai prefix",
			parse:  registry.ParseVisible,
			body:   "!ai stop",
			wantOK: false,
		},
		{
			name:   "unknown command",
			parse:  registry.ParseVisible,
			body:   "/unknown",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.parse(tt.body)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("parsed %#v, want %#v", got, tt.want)
			}
		})
	}
}
