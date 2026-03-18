# discord-bot-plugins

Official plugins for the [`discord-`](https://github.com/qqqyyyhhh8-del/discord-) bot host.

These plugins require host version `v0.5.0` or newer.

## Included Plugins

- `plugins/persona`: persona management panel with prompt injection
- `plugins/proactive`: proactive reply panel and proactive message hook
- `plugins/emoji`: guild emoji analysis panel with 4x4 sheet analysis and worldbook updates

## Install Examples

- Persona:
  `/plugin install repo:https://github.com/qqqyyyhhh8-del/discord-bot-plugins.git path:plugins/persona`
- Proactive:
  `/plugin install repo:https://github.com/qqqyyyhhh8-del/discord-bot-plugins.git path:plugins/proactive`
- Emoji:
  `/plugin install repo:https://github.com/qqqyyyhhh8-del/discord-bot-plugins.git path:plugins/emoji`

After installation, the host automatically registers:
- `/persona`
- `/proactive`
- `/emoji`

Each plugin ships as an external-process plugin using the host's JSON-RPC over stdio protocol.

## Development

```bash
go test ./...
go build ./...
```

The shared plugin SDK used by this repository lives in `sdk/pluginapi`.

## License

MIT
