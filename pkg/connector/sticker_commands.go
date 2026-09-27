package connector

import "maunium.net/go/mautrix/bridgev2/commands"

var commandStickers = &commands.FullHandler{
	Name: "stickers",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionMisc,
		Description: "Add your LINE sticker packs to this room and refresh them",
	},
	RequiresPortal: true,
	RequiresLogin:  true,
	Func: func(ce *commands.Event) {
		login, _, err := ce.Portal.FindPreferredLogin(ce.Ctx, ce.User, false)
		if err != nil || login == nil {
			ce.Reply("Could not find your LINE login for this room.")
			return
		}
		client, ok := login.Client.(*LineClient)
		if !ok {
			ce.Reply("This room does not use a LINE login.")
			return
		}
		client.resetStickerCatalogs()
		if err = client.enableRoomStickers(ce.Ctx, ce.RoomID); err != nil {
			ce.Reply("Failed to synchronize LINE sticker packs: %s", err)
			return
		}
		ce.Reply("LINE sticker packs are now available in this room. Pack images are not end-to-end encrypted.")
	},
}
