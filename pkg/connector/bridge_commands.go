package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
)

var aiCommandHelpSection = commands.HelpSection{Name: "AI rooms", Order: 21}

type aiCommandProcessor interface {
	AddHandlers(...commands.CommandHandler)
}

type bridgeCommandResponder struct {
	ce *commands.Event
}

func (r bridgeCommandResponder) Reply(_ context.Context, text string) error {
	r.ce.Reply(text)
	return nil
}

func (c *Connector) registerAICommands() {
	if c == nil || c.Bridge == nil || c.Bridge.Commands == nil {
		return
	}
	processor, ok := c.Bridge.Commands.(aiCommandProcessor)
	if !ok {
		return
	}
	processor.AddHandlers(
		c.bridgeAICommand("model", "Show or set the AI model for this room.", "[model]"),
		c.bridgeAICommand("reasoning", "Show or set the reasoning level for this room.", "[off|minimal|low|medium|high|xhigh]"),
		c.bridgeAICommand("system-prompt", "Show, set, or clear this room's additional system prompt.", "[prompt|clear]"),
		c.bridgeAICommand("ai-help", "Show available AI Bridge commands.", "[command]"),
	)
}

func (c *Connector) bridgeAICommand(name, description, args string) *commands.FullHandler {
	return &commands.FullHandler{
		Func: func(ce *commands.Event) {
			c.handleBridgeAICommand(ce)
		},
		Name: name,
		Help: commands.HelpMeta{
			Section:     aiCommandHelpSection,
			Description: description,
			Args:        args,
		},
		RequiresPortal: true,
		RequiresLogin:  true,
	}
}

func (c *Connector) handleBridgeAICommand(ce *commands.Event) {
	if ce == nil {
		return
	}
	defName := ce.Command
	if defName == "ai-help" {
		defName = "help"
	}
	def, ok := aiSlashCommandByName(defName)
	if !ok {
		ce.Reply("Unknown AI command.")
		return
	}
	cl, err := c.clientForBridgeAICommand(ce)
	if err != nil {
		ce.Reply(err.Error())
		return
	}
	arg := strings.TrimSpace(ce.RawArgs)
	if def.argRequired && arg == "" {
		ce.Reply(aiSlashCommandUsage(def))
		return
	}
	var roomConfig RoomConfig
	if def.needsRoomConfig {
		roomConfig, _, err = c.ReadRoomConfig(ce.Ctx, ce.Portal.MXID)
		if err != nil {
			ce.Log.Err(err).Msg("Failed to read AI room state for command")
			ce.Reply("Failed to read AI room settings.")
			return
		}
	}
	err = def.run(cl, ce.Ctx, ce.Portal, roomConfig, arg, bridgeCommandResponder{ce: ce})
	if err != nil {
		if def.noticeErrors {
			ce.Reply(err.Error())
		} else {
			ce.Log.Err(err).Msg("AI command failed")
			ce.Reply("AI command failed: %v", err)
		}
	}
}

func (c *Connector) clientForBridgeAICommand(ce *commands.Event) (*Client, error) {
	if ce.Portal == nil {
		return nil, errors.New("This command can only be used in AI portal rooms.")
	}
	login, _, err := ce.Portal.FindPreferredLogin(ce.Ctx, ce.User, false)
	if errors.Is(err, bridgev2.ErrNotLoggedIn) {
		return nil, errors.New("You're not logged in for this AI room.")
	} else if err != nil {
		ce.Log.Err(err).Msg("Failed to find preferred AI login for command")
		return nil, errors.New("Failed to find the AI login for this room.")
	}
	cl, ok := login.Client.(*Client)
	if !ok {
		return nil, fmt.Errorf("Login %s is a provider account, not the Beeper AI room login.", login.ID)
	}
	return cl, nil
}
