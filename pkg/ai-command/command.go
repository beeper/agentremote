package aicommand

import "strings"

const MatrixCommandMsgType = "com.beeper.command"

type Command struct {
	Name string
	Arg  string
}

type Registry struct {
	names   map[string]struct{}
	aliases map[string]string
}

func NewRegistry(names []string, aliases map[string]string) Registry {
	registry := Registry{
		names:   map[string]struct{}{},
		aliases: map[string]string{},
	}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			registry.names[name] = struct{}{}
		}
	}
	for alias, canonical := range aliases {
		alias = strings.ToLower(strings.TrimSpace(alias))
		canonical = strings.ToLower(strings.TrimSpace(canonical))
		if alias != "" && canonical != "" {
			registry.aliases[alias] = canonical
		}
	}
	return registry
}

func (r Registry) ParseVisible(body string) (Command, bool) {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "/") {
		return Command{}, false
	}
	return r.parseNamed(strings.TrimPrefix(body, "/"))
}

func (r Registry) ParseHidden(body string) (Command, bool) {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "/") {
		return r.ParseVisible(body)
	}
	if !strings.HasPrefix(body, "!ai ") {
		return Command{}, false
	}
	return r.parseNamed(strings.TrimSpace(strings.TrimPrefix(body, "!ai")))
}

func (r Registry) CanonicalName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if canonical := r.aliases[name]; canonical != "" {
		return canonical
	}
	return name
}

func (r Registry) parseNamed(body string) (Command, bool) {
	name, arg, _ := strings.Cut(strings.TrimSpace(body), " ")
	name = r.CanonicalName(name)
	arg = strings.TrimSpace(arg)
	if _, ok := r.names[name]; ok {
		return Command{Name: name, Arg: arg}, true
	}
	return Command{}, false
}
