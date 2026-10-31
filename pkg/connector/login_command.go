package connector

import (
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"
)

func (lc *LineConnector) registerLoginCommand() {
	if processor, ok := lc.br.Commands.(*commands.Processor); ok {
		processor.AddHandler(lineLoginCommand())
	}
}

func lineLoginCommand() *commands.FullHandler {
	// Copy the framework handler to retain its permission requirements without
	// modifying the shared handler. Only method selection is LINE-specific.
	handler := *commands.CommandLogin
	handler.Help.Args = "[qr|email|help]"
	handler.Func = func(ce *commands.Event) {
		flows := ce.Bridge.Network.GetLoginFlows()
		var help strings.Builder
		help.WriteString("Choose a LINE login method:\n\n")
		for _, flow := range flows {
			method := flow.ID
			switch flow.ID {
			case LoginFlowIDQR:
				method = "qr"
			case LoginFlowIDEmail:
				method = "email"
			}
			_, _ = fmt.Fprintf(&help, "* `$cmdprefix login %s` — %s\n", method, flow.Description)
		}
		if len(ce.Args) == 0 || strings.EqualFold(ce.Args[0], "help") {
			ce.Reply(help.String())
			return
		}
		method := strings.ToLower(ce.Args[0])
		switch method {
		case "qr":
			method = LoginFlowIDQR
		case "email":
			method = LoginFlowIDEmail
		}
		for _, flow := range flows {
			if strings.EqualFold(flow.ID, method) {
				ce.Args = append([]string{flow.ID}, ce.Args[1:]...)
				commands.CommandLogin.Run(ce)
				return
			}
		}
		ce.Reply("Unknown or unavailable LINE login method.\n\n" + help.String())
	}
	return &handler
}
