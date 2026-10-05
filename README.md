# Microsoft Teams → Beeper

A local Go bridge for Microsoft Teams **work/school accounts**, using delegated Microsoft Graph access and mautrix bridgev2. Runs on macOS while Beeper is open. Requires your own Microsoft app registration and Beeper bridge configuration.

## Start and sign in

1. Open Beeper.
2. Double-click `Start-Work-Teams.command`. Keep its Terminal window open. It starts the bridge while Beeper is running and stops the bridge when Beeper closes. Quit with Control-C. Do not run a second copy while one is active.
3. In Beeper, open a chat with `@sh-teamsworkbot:beeper.local`, send `login`, and choose the login flow matching your approved delegated permissions.
4. Open the displayed Microsoft sign-in link in a browser on this Mac and sign in with your work account. Approve the app permissions if your organization allows it.
5. Existing direct/group chats should appear. Initial import is up to 50 recently modified messages per chat. Recent chats are checked every 5 seconds plus processing time, independently of full discovery and history import. Unchanged recent chats are rechecked every minute; full discovery runs every 10 minutes after the previous scan completes. Rate limits and busy chats can increase the delay.

For manual foreground operation, run `./scripts/run.sh`. For the app-aware launcher, run `python3 scripts/while-beeper-open.py`. The launcher itself is not installed as a login item and must be started each session. This is a locally hosted bridge connected to Beeper's Matrix service; it is not an on-device bridge embedded inside the Beeper app.

## Microsoft app registration

The `network` section of the private `config.yaml` contains the supplied client and tenant IDs. Browser sign-in supports either a public client without a secret or a Web registration with a locally stored client secret.

In Microsoft Entra → App registrations → your app:

- Authentication → Add a platform → **Mobile and desktop applications** → register `http://localhost:29319/oauth/callback` for browser sign-in with PKCE. This public-client flow uses no secret. If your callback is registered under **Web**, keep that registration and use `Set-Client-Secret.command` to enter the secret **value** locally. It is saved with owner-only permissions in `.secrets/client-secret`, excluded from Git, and read for token exchange and refresh. Never share that file.
- For the optional device-code fallback: Authentication → Advanced settings → **Allow public client flows: Yes**.
- API permissions → Microsoft Graph → **Delegated permissions**: `User.Read`, `Chat.Read`, `ChatMessage.Send`, and `offline_access`. The profile-photo flow additionally requests `User.ReadBasic.All`.
- Grant administrator consent if required by your organization's policies. Application-only permissions do not substitute for delegated sign-in when sending ordinary chat messages.

Device-code login can be blocked by organizational Conditional Access. If Microsoft reports that, the organization must permit an appropriate sign-in flow; this bridge does not bypass policy. The browser flow uses a loopback-only listener on port 29319, one-time OAuth state, S256 PKCE, and a ten-minute expiry. It remains subject to Conditional Access.

## What works in this version

- Tenant-scoped Microsoft browser sign-in with PKCE, optional device-code sign-in, and refresh-token rotation.
- Discovery of existing direct and group chats, paginated membership, participant names, and profile photos. Photos require delegated `User.ReadBasic.All`; after granting it, use `login work_photos` to consent. Missing or inaccessible photos preserve names. Photo results are cached to limit requests.
- Teams → Beeper text (HTML rendered as readable text) and attachment links.
- Beeper → Teams plain text in existing chats.
- Persistent message IDs and sync cursors; cursor advancement waits for durable Matrix delivery.
- Catch-up pagination after an initial import; overlapping polling windows and durable deduplication.
- Native Beeper backward history insertion with paginated Graph cursors and duplicate detection.
- Official Teams icon from Microsoft’s Teams web app (`assets/teams.png`).
- Graph throttling honors `Retry-After`. Sends are not automatically retried after an uncertain response.
- Beeper websocket connection and end-to-bridge encryption through mautrix.

## Current limitations

Automatic channel discovery, meeting chats, new chat creation, edits, deletion propagation, reactions, typing, read receipts, general file uploads/downloads, rich cards are not implemented. Attachments are links that may require a Microsoft sign-in. HTML formatting is converted to text/Markdown. The first import is bounded to 50 messages. Chat discovery prioritizes most recently active conversations. Ready history tasks are prioritized in that same order, preserving retry delays. The backward history queue then requests older pages, 50 messages per batch with a 20-second delay, until Graph has no more available history. Progress is persisted by bridgev2. Old messages that are edited after the initial import may appear when Graph returns them, but edits to already bridged messages are ignored.

A successful build and mock tests do not establish that your tenant will grant access. Live verification requires Microsoft sign-in, successful chat discovery, an incoming Teams message appearing in Beeper, and a user-initiated Beeper message reaching Teams. Do not use automated test messages to real colleagues.

## Build and test

Go's module file selects Go 1.25.6. Dependencies and toolchain are pinned in `go.mod` / `go.sum`.

```sh
./scripts/build.sh
go test -race -tags goolm ./...
```

The binary uses the pure-Go Olm implementation, so local libolm headers are unnecessary. The build script keeps its cache in `.cache/go-build`.

## Configuration and data

`config.yaml` was generated with Beeper Bridge Manager, then configured with your Microsoft app details. It contains Beeper credentials: **do not share it or commit it**. SQLite, logs, and configuration may contain work messages and delegated credentials. Launchers set a private file-creation mask and the config is mode 600. They are excluded from Git. Tokens are stored in the bridge database, not macOS Keychain; protect the Mac and its backups accordingly.

To generate a fresh Beeper registration on another machine/account:

```sh
bbctl login
bbctl config --type bridgev2 --output config.yaml sh-teamswork
```

Add the following network section, using your registration details:

```yaml
network:
    client_id: YOUR-APPLICATION-CLIENT-ID
    tenant_id: YOUR-DIRECTORY-TENANT-ID
    poll_seconds: 5
    initial_messages: 50
```

Use `127.0.0.1` for the appservice hostname, disable provisioning debug endpoints, and set logging `min_level: info`. The Beeper websocket setup does not require public inbound ports. Existing `sh-teams` and `sh-msteams` registrations are untouched.

## Sources

- [Beeper self-hosting and bridgev2 setup](https://developers.beeper.com/bridges/self-hosting/)
- [Microsoft Graph: list chats](https://learn.microsoft.com/en-us/graph/api/chat-list?view=graph-rest-1.0)
- [Microsoft Graph: list chat messages](https://learn.microsoft.com/en-us/graph/api/chat-list-messages?view=graph-rest-1.0)
- [Microsoft Graph: send messages](https://learn.microsoft.com/en-us/graph/api/chatmessage-post?view=graph-rest-1.0)
- [Microsoft device authorization flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-device-code)
- [mautrix-go](https://github.com/mautrix/go): Matrix framework, under MPL-2.0.

`upstream-teams/` is an unmodified reference checkout of [Beeper's consumer Teams bridge](https://github.com/beeper/teams), under its own AGPL license. It is not imported or compiled into this work-account connector. The dependency baseline was taken from its module configuration.

Inline Teams hosted images are downloaded through Graph and uploaded as native Beeper image messages, including encrypted rooms, captions, and dimensions (PNG/JPEG/GIF, up to 20 MiB). This applies to new messages and remaining history imports; previously imported text placeholders are not rewritten. External images and SharePoint file attachments remain unsupported as inline media.

Direct/group chat replies preserve message references in Beeper during live sync and remaining backfill, with readable quote text if the original has not imported yet. Beeper replies are sent via Graph v1.0 `replyWithQuote` with the existing ChatMessage.Send permission. Missing or cross-chat reply targets fail explicitly rather than sending an unrelated top-level message. Previously imported messages are not rewritten. Selected channels support posts, threaded replies, hosted images, and paginated history.

Person @mentions are supported in both directions, including quoted replies. Select a person from Beeper’s mention picker to send a real Teams mention; plain typed @names are not guessed. Incoming Teams @Everyone mentions carry room mention metadata; sending @everyone is currently unsupported. Mention support applies to new messages and remaining backfill, not already imported messages. No additional Graph permissions are required.

If Beeper supplies mention identities without formatted links, the bridge preserves the message text and appends explicit Teams tags for those users, using saved display names. It does not infer identities from typed names.

Outgoing GIF/PNG/JPEG attachments use Graph hosted content (4 MB limit), including encrypted Beeper media, captions, mentions, and replies. GIF bytes are preserved. MP4 animations marked as GIFs are converted locally with ffmpeg when available; ordinary video uploads are not supported. No additional Microsoft permission is needed.

## Login forms and approved permissions

| Flow | Requested permissions beyond offline access and User.Read | Inputs |
| --- | --- | --- |
| `work_readonly` | Chat.Read | App ID, tenant ID, browser authentication method |
| `work_browser` | Chat.Read, ChatMessage.Send | App ID, tenant ID, browser authentication method |
| `work_photos` | Chat.Read, ChatMessage.Send, User.ReadBasic.All | App ID, tenant ID, browser authentication method |
| `work_device_code` | Chat.Read, ChatMessage.Send | App ID and tenant ID; public-client device-code flow |

Browser authentication choices are `saved_secret` (configured registration only), `new_secret` (a second password form), and `public_client` (no secret). New secrets are stored in owner-only files under `.secrets/`; token refresh uses the same app and tenant as sign-in. Photo fetching is enabled only for the photo flow, and read-only logins refuse sends. Existing saved logins retain their original settings until reauthenticated. These choices request permissions; they do not grant administrator approval or bypass Conditional Access. The preset login forms do not request channel permissions. Selected channels require an existing delegated token with Channel.ReadBasic.All and ChannelMessage.Read.All; sending additionally requires ChannelMessage.Send.


## Selected Teams channels

Add channels explicitly under `network.channels` in private `config.yaml`, then restart:

```yaml
network:
  channels:
    - tenant_id: YOUR-TENANT-ID
      team_id: TEAM-GROUP-ID
      channel_id: 19:CHANNEL-ID@thread.tacv2
      name: Team — Channel
```

The Teams channel link supplies the `groupId` and encoded channel ID. The bridge checks the latest 50 reply chains every 15 seconds plus processing time and backs off on errors. Older chains are imported through the history queue. Graph orders chains by their latest activity, so this is not strict post-creation order. Channel membership discovery and changes to already imported messages are not synchronized.
