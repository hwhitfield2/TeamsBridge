# Microsoft Teams → Beeper

A local Go bridge for Microsoft Teams **work/school accounts**, using delegated Microsoft Graph access and mautrix bridgev2. Runs on macOS while Beeper is open. Requires your own Microsoft app registration and Beeper bridge configuration.

## Start and sign in

1. Open Beeper.
2. Double-click `Start-Work-Teams.command`. Keep its Terminal window open. It starts the bridge while Beeper is running and stops the bridge when Beeper closes. Quit with Control-C. Do not run a second copy while one is active.
3. In Beeper, open a chat with `@sh-teamsworkbot:beeper.local`, send `login`, and choose the login flow matching your approved delegated permissions.
4. Open the displayed Microsoft sign-in link in a browser on this Mac and sign in with your work account. Approve the app permissions if your organization allows it.
5. Existing direct/group and meeting chats should appear. Initial import is up to 50 recently modified messages per chat. Recent chats are checked every 5 seconds plus processing time, independently of full discovery and history import. Unchanged recent chats are rechecked every minute; full discovery runs every 10 minutes after the previous scan completes. Rate limits and busy chats can increase the delay.

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
- Discovery of existing direct, group, and meeting chats, paginated membership, participant names, and profile photos. Photos require delegated `User.ReadBasic.All`; after granting it, use `login work_photos` to consent. Missing or inaccessible photos preserve names. Photo results are cached to limit requests.
- Teams → Beeper formatted text (bold, lists, quotes, links, and code) and attachment links.
- Beeper → Teams text, replies, Unicode reactions, text edits, and deletion of your own messages.
- Create direct chats by user ID or Microsoft sign-in address, and group chats with selected participants.
- Synchronize your own chat read position; this does not report whether coworkers have read your messages.
- Readable Adaptive/Hero/Thumbnail card text, facts, and links, including app-authored messages.
- Optional OneDrive/SharePoint file uploads and downloads up to 20 MB, with encrypted Matrix media support. Custom reaction images use unencrypted Matrix media assets, as required for MXC reaction keys.
- Persistent message IDs and sync cursors; cursor advancement waits for durable Matrix delivery.
- Catch-up pagination after an initial import; overlapping polling windows and durable deduplication.
- Native Beeper backward history insertion with paginated Graph cursors and duplicate detection.
- Official Teams icon from Microsoft’s Teams web app (`assets/teams.png`).
- Graph throttling honors `Retry-After`. Sends are not automatically retried after an uncertain response.
- Beeper websocket connection and end-to-bridge encryption through mautrix.

## Current limitations

Automatic channel discovery, live typing indicators, other participants' read receipts, meeting scheduling/calls, interactive card forms/actions, and files over 20 MB are not implemented. Graph does not document a delegated user typing endpoint; the `typing` enum alone does not provide an implementation. Rich cards are rendered as readable text and links, rather than interactive Teams widgets. Card submit/input actions remain in Teams.

Edits and deletions in Teams are reconciled when the changed message is returned by polling. Recent chats are checked at least once per minute; older chats depend on full discovery. Selected channels inspect the latest 50 active reply chains, so changes outside that window can be missed. The bridge edits text from Beeper and deletes only your own messages. Channel editing accepts `ChannelMessage.Edit` or `ChannelMessage.ReadWrite`; Microsoft's update endpoint currently documents the latter, so a tenant/API rejection with Edit alone may require ReadWrite. Channel deletion requires ReadWrite. Incoming custom image reactions use Matrix media URLs and readable shortcodes. Outgoing reactions support Unicode emoji; adding/removing custom image reactions still requires Teams because Graph documents only Unicode reaction changes.

The first import is bounded to 50 messages (50 reply chains for channels). Chat discovery prioritizes recently active conversations, and the history queue imports older pages with a 20-second batch delay. Historical file attachment placeholders are not automatically replaced after granting file access. A one-time renderer repair updates existing text-only bot messages (including Facilitator) in place and revisits GIFs and custom reactions in the newest 50 messages of the newest 50 chats; configured channels are repaired as their active reply chains are polled. Unavailable or oversized downloads retain their original links. Uploads use unique filenames: chat files go into your OneDrive and grant read access only to current chat participants without email invitations; channel files go into the channel's existing files folder. If sharing or sending fails after upload, the file can remain in OneDrive/SharePoint; the bridge reports failures and does not blindly resend.

Read-state synchronization covers your own account. Opening the newest bridged chat message in Beeper marks the chat read in Teams; reading older history does not intentionally mark newer messages read. Graph's action marks the whole chat rather than an exact message and cannot eliminate a race with a newly arriving message. Incoming personal read positions come from `chat.viewpoint`. Read-state sync does not apply to channels.


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

Inline Teams hosted images are downloaded through Graph and uploaded as native Beeper image messages, including encrypted rooms, captions, and dimensions (PNG/JPEG/GIF, up to 20 MiB). This applies to new messages and remaining history imports; previously imported text placeholders are not rewritten. External GIFs from the approved Giphy and Tenor media hosts are imported with their animation intact; other arbitrary HTML image URLs are not fetched. File references can be imported as native file attachments with the file permissions described below.

Incoming images and GIFs retain their position among the text blocks in a Teams message, including forwarded messages. Existing messages can be edited in place only when the edit does not add new rendered parts: Beeper cannot insert extra events into the middle of an existing timeline. Rebuild an affected chat copy to change its historical layout; automatic renderer upgrades do not append or prepend missing parts.

Direct/group chat replies preserve message references in Beeper during live sync and remaining backfill, with readable quote text if the original has not imported yet. Beeper replies are sent via Graph v1.0 `replyWithQuote` with the existing ChatMessage.Send permission. Missing or cross-chat reply targets fail explicitly rather than sending an unrelated top-level message. Selected channels support posts, threaded replies, hosted images, and paginated history.

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
| `work_extended` | Chat.Read, ChatMessage.Send, Chat.ReadWrite, Chat.Create, User.ReadBasic.All | Browser registration and authentication |
| `work_files` | All `work_extended` permissions plus Files.ReadWrite | Browser registration and authentication |
| `work_channels` | All `work_extended` permissions plus Channel.ReadBasic.All, ChannelMessage.Read.All, ChannelMessage.Send, ChannelMessage.ReadWrite, Files.ReadWrite.All | Browser registration and authentication |

Browser authentication choices are `saved_secret` (configured registration only), `new_secret` (a second password form), and `public_client` (no secret). New secrets are stored in owner-only files under `.secrets/`; token refresh uses the same app and tenant as sign-in. Photo fetching is enabled for the photo and extended flows, and read-only logins refuse sends. Existing saved logins retain their original settings until reauthenticated. These choices request permissions; they do not grant administrator approval or bypass Conditional Access. Selected channels require Channel.ReadBasic.All and ChannelMessage.Read.All; sending and reactions require ChannelMessage.Send. Adding permissions in Entra is not enough: consent and a fresh sign-in must put them in the access token. Existing logins continue to use already granted scopes; do not reauthenticate just to enable a feature whose permission is already present.


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

The Teams channel link supplies the `groupId` and encoded channel ID. The bridge checks the latest 50 reply chains every 15 seconds plus processing time and backs off on errors. Older chains are imported through the history queue. Graph orders chains by their latest activity, so this is not strict post-creation order. Channel membership discovery is not synchronized. Edits, deletions, and reactions within the polled reply chains are reconciled.


## Feature API references

- [Edit messages](https://learn.microsoft.com/en-us/graph/api/chatmessage-update?view=graph-rest-1.0) and [delete messages](https://learn.microsoft.com/en-us/graph/api/chatmessage-softdelete?view=graph-rest-1.0).
- [Set reactions](https://learn.microsoft.com/en-us/graph/api/chatmessage-setreaction?view=graph-rest-1.0) and [remove reactions](https://learn.microsoft.com/en-us/graph/api/chatmessage-unsetreaction?view=graph-rest-1.0).
- [Create chats](https://learn.microsoft.com/en-us/graph/api/chat-post?view=graph-rest-1.0), [mark chats read](https://learn.microsoft.com/en-us/graph/api/chat-markchatreadforuser?view=graph-rest-1.0), and [personal read positions](https://learn.microsoft.com/en-us/graph/api/resources/chatviewpoint?view=graph-rest-1.0).
- [Upload files](https://learn.microsoft.com/en-us/graph/api/driveitem-put-content?view=graph-rest-1.0), [grant recipient access](https://learn.microsoft.com/en-us/graph/api/driveitem-invite?view=graph-rest-1.0), and [send reference attachments/cards](https://learn.microsoft.com/en-us/graph/api/chatmessage-post?view=graph-rest-1.0).

New direct chats are available through the bridge's `start-chat <user ID or sign-in address>` bot command and provisioning API. Group creation is exposed through bridgev2's group-creation provisioning API; Beeper client versions may differ in whether they show that control. Creating meeting chats is not supported; existing meeting chats are discovered automatically.

### Push notifications through an HTTPS tunnel

Push delivery is optional. Each installation supplies its own public HTTPS origin
in the private `config.yaml` (which is ignored by Git):

```yaml
network:
    webhook_url: https://teams.example.com
    webhook_listen: 127.0.0.1:29320
```

Keep the other `network` settings. Leave `webhook_url` blank to disable push.
Forward the tunnel to `http://127.0.0.1:29320`. Microsoft must be able to POST to
`/graph/notifications` without an access-login screen or bot challenge; the same
path handles endpoint validation and subscription lifecycle events. `/healthz`
returns `ok` when the listener is running. This is a separate listener from OAuth
and the Matrix appservice; do not expose their ports through this tunnel.

After restarting, the bridge creates a delegated user-wide subscription for all
chats (including group and meeting chats) and a subscription for each configured
channel. Existing `Chat.Read`/`Chat.ReadWrite` and `ChannelMessage.Read.All` grants
are used. User-wide subscriptions use Microsoft's documented **beta** subscription
endpoint; channel subscriptions and message retrieval use v1.0. If Microsoft
rejects a subscription, the bridge logs the failure and continues polling.

Notifications trigger an immediate fetch of the changed message, including
edits, deletion and reaction updates. Channel replies use their own message
resource, even when their parent falls outside the recent-channel polling window.
Microsoft reports typical message notification latency below 10 seconds, with up
to a minute expected; this is near-real-time delivery, not a guarantee of instant
arrival. Sending from Beeper continues to use the existing immediate send path.

The bridge verifies the subscription ID, tenant, random client-state secret and
resource scope, saves accepted notifications to its database before acknowledging
them, and retries failed processing after restart. Subscription IDs and secrets
are stored in private login metadata. Two-hour subscriptions are renewed roughly
every 30 minutes, and lifecycle notifications trigger reauthorization or recovery.
Existing polling stays enabled to catch missed notifications and tunnel outages.
Beeper, the bridge, and the tunnel must remain running for push delivery. A stable
tunnel hostname avoids replacement subscriptions. To change it, update the local
`webhook_url` and restart; only this bridge's saved subscriptions are replaced.
Disabling push or stopping the bridge lets its subscriptions expire within two
hours; reopening restores them automatically.

Verification: check the public `/healthz`, then look for `Teams push subscription
active` and `Teams push update delivered` in `logs/bridge.log`. Health only confirms
the listener, not subscription approval. Do not share the database, tokens,
client-state values, or private config when reporting a problem.

References: [Teams message notifications](https://learn.microsoft.com/en-us/graph/teams-changenotifications-chatmessage),
[webhook delivery](https://learn.microsoft.com/en-us/graph/change-notifications-delivery-webhooks),
and [subscription lifetime and latency](https://learn.microsoft.com/en-us/graph/api/resources/subscription?view=graph-rest-1.0).
