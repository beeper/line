# LINE client settings that change what a message looks like

LINE's own apps have settings that decide how a received message is shown or
played. Beeper has no equivalent. Read this before you test a feature that
touches media, previews, or notifications.

Verified on 2026-10-25 with LINE iOS and LINE Chrome Extension 3.7.2.

## Why it matters

The same message can look different on two LINE clients. A GIF can autoplay on
one and wait for a tap on another, with no bridge bug involved. If you do not
know which setting is on, you can read a normal result as a bug, or miss a real
one.

## Settings by area

"Unknown" means nobody has checked it yet. Do not assume it matches iOS.

| Setting | What it does | iOS | Android | Desktop | Chrome extension | Beeper equivalent | What to test |
|---|---|---|---|---|---|---|---|
| Photo quality (Settings → Photos and videos) | Standard or High for photos you send | Yes | Unknown | Unknown | `Photo resolution` in Settings → Chats (default `Original`; photos over 20 MB can't be sent in their original resolution) | None | Photos at both values. Does not change GIFs: the original file is sent either way |
| Auto-download images | Downloads received images on its own | Yes | Unknown | Unknown | Unknown | None | With it off, media sent from another device waits for a tap before it downloads |
| Autoplay GIFs | Plays received GIFs without a tap | Yes | Unknown | Unknown | `Animated GIFs`: "Auto-play animated GIFs in chats" | None | GIF on and off. See the sender and receiver rules below |
| Autoplay videos | Plays received videos without a tap | Yes | Unknown | Unknown | Unknown | None | Check that a GIF does not arrive as a video, because this setting would then control it |
| Send Live Photos | Sends iOS Live Photos as such | Yes | Unknown | Unknown | Unknown | None | Only for Live Photo work. Not about GIFs |
| File previews | Preview of attached files | Unknown | Unknown | Unknown | Settings → Chats | None | Files of different types |
| URL previews | Preview of links | Unknown | Unknown | Unknown | Settings → Chats | None | A link with and without a preview |
| Font size, Send key, Chat folders | Layout and input | Unknown | Unknown | Unknown | Settings → Chats | None | Rarely needed |
| Block messages from non-friends | Drops messages from people who are not friends | Unknown | Unknown | Unknown | Settings → Chats | None | Messages from a non-friend, if your feature involves one |
| Notifications: pop-up, sound, show preview, rules | What an incoming message does | Unknown | Unknown | Unknown | Settings → Notifications | Beeper has its own | Only if your feature changes notification text |
| Turn off mobile notifications when logged in to the Chrome version | Mutes the phone while the extension is logged in | Unknown | Unknown | Unknown | Settings → Notifications | None | Remember the bridge acts as the Chrome extension, so this can mute the phone |

## Sender versus receiver

- Playback and auto-download settings apply to what the device **receives**.
  A test that only looks at your own sent message can miss them. Photo quality
  and Send Live Photos apply on the sending device.
- LINE iOS autoplays a GIF only if it was sent from that phone. A GIF sent from
  another device of the same account, whether the Chrome extension or the
  bridge, needs a tap there. So a bridge-sent GIF that does not autoplay on your
  own phone is not a bug.
- The Chrome extension autoplays all GIFs.
- With Auto-download images off, media from another device waits for a tap
  before it downloads.
- The real receiver test is a second account. One tester cannot check it.

## Testing without a second account

- Use the Chrome extension as a separate view of your own chat. It receives
  messages with no cached file. LINE allows only one Chrome extension session,
  so logging into the extension logs the bridge out. Quit Chrome and reconnect
  the bridge in Beeper when you finish.
- Record each LINE setting before you start and put it back afterwards.
- Do not use a LINE Official Account bot that relays messages through the
  Messaging API as a receiver for media tests. It re-sends with its own
  metadata and flattens GIFs to one frame.

## Always capture what the Chrome extension does first

The bridge copies the Chrome extension, so the extension is the reference. Before
you write code for a feature, capture what the extension sends and receives, and
match that. The tools and the workflow are in
[beeper-line-skills](https://github.com/clins1994/beeper-line-skills).
