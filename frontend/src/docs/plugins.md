# Plugins

A plugin adds something of your own to Nightjar. A plugin can provide any of these:

- **A check type.** It is up or down like any uptime check, with its own history and alerts.
- **A card on the overview.** It shows a few values or a line of text, refreshed as often as the plugin asks.
- **An alert channel.** Alerts go through the plugin, for a service Nightjar doesn't support itself.

A plugin is a folder holding a `plugin.json` and a program. The program can be a shell script, Python or anything else the machine can run. Nightjar runs it and reads what it prints.

## Install one

1. Put the plugin's folder in the plugins folder. The default is `plugins` in Nightjar's working folder, beside its database. Set `PLUGINS_DIR` under **[Settings → Storage and logging](/settings)** to use another one.
2. Open **[Plugins](/plugins)** and press **Look again**.
3. Fill in the plugin's settings, then turn it on. **Try the check** and **Try the card** run it once and show what it said, even while it is off.

A new plugin starts off. Nightjar runs a plugin only while it is on.

The Nightjar source has three examples in `examples/plugins`:

- **System load** is a card with the load averages.
- **DNS lookup** is a check that a host name resolves.
- **Alert log** is a channel that writes alerts to a file.

Copy them into the plugins folder to try them.

## Using what a plugin provides

- **Checks:** in **[Services](/services)**, add a check of type **plugin**. Its target is the plugin's id, such as `dns-resolve`. A plugin that takes an argument has it after a colon, as `dns-resolve:example.com`.
- **Cards:** they show on the **[Overview](/)** once the plugin is on.
- **Alert channels:** a channel appears in **[Notifications](/notifications)** with the plugin's name, where you choose what it hears about. Nightjar sets alert channels up when it starts, so when **Look again** finds one added, renamed or removed, it restarts by itself, and the page reconnects. A channel that is off hears nothing.

## Safety

A plugin runs as the same user as Nightjar, so install only plugins you trust. Nightjar won't run a plugin when:

- other users could change it, because its folder, `plugin.json` or program is group- or world-writable
- it belongs to a user other than root or Nightjar's own user
- its program is outside its folder

Each run gets the plugin's settings and little else from Nightjar's environment. It is stopped, with anything it started, when it takes too long.

Secret settings are saved but never shown again; leave one empty to keep it.

## Writing a plugin

### plugin.json

```json
{
  "id": "dns-resolve",
  "name": "DNS lookup",
  "version": "1.0.0",
  "description": "A check that is up while a host name resolves.",
  "author": "You",
  "provides": ["check"],
  "run": "run.sh",
  "timeout": 10,
  "check": { "argument": "Host name", "placeholder": "example.com" },
  "settings": [{ "key": "EXPECT", "label": "Expected address", "help": "Empty accepts any address." }]
}
```

| Field             | What                                                                                                                   |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `id`              | lowercase letters, digits and dashes; used in check targets                                                            |
| `provides`        | any of `check`, `card` and `notify`                                                                                    |
| `run`             | the program, relative to the folder; it must be executable                                                             |
| `timeout`         | seconds one run may take, 20 by default, at most 120; a check also stops at its own timeout                            |
| `interval`        | how often a card refreshes, in seconds; 300 by default, at least 30                                                    |
| `check`           | what the check's argument is called, if it takes one                                                                   |
| `settings`        | the values to fill in: `key` (capitals and underscores), `label`, `type`, `default`, `help`, `placeholder`, `required` |
| `settings[].type` | `text` (the default), `secret`, `number`, `bool`, or `select` with `options`                                           |

### What the program gets

It is run with one argument, `check`, `card` or `notify`, in its own folder. The same information comes two ways:

- On **standard input** as JSON: `{"mode", "settings": {...}, "argument", "alert"}`.
- In the **environment**:
  - `NIGHTJAR_MODE`, `NIGHTJAR_ARGUMENT` and `NIGHTJAR_PLUGIN_DIR`
  - `NIGHTJAR_SETTING_<KEY>` for each setting
  - for an alert, `NIGHTJAR_ALERT_TITLE`, `NIGHTJAR_ALERT_DESCRIPTION`, `NIGHTJAR_ALERT_SEVERITY` and `NIGHTJAR_ALERT_CATEGORY`

### What it prints

- **A check** prints `{"up": true, "detail": "example.com is 93.184.215.14"}`, optionally with `"latency_ms"`. Or it prints anything and exits 0 when up and non-zero when down; its last line is the detail.
- **A card** prints this:

  ```json
  { "title": "Internet", "items": [{ "label": "Down", "value": "930 Mbps", "tone": "good" }], "text": "", "link": "https://…" }
  ```

  A `tone` is `good`, `warn` or `crit`. A card shows up to 12 items. A program that prints plain text gets a card with that text.

- **An alert channel** gets the alert as JSON: `title`, `description`, `severity` (`info`, `ok`, `warning` or `critical`), `fields`, `host`, `category`, `dashboard_url` and `time`. Exiting 0 means it was delivered. Anything else is a failed delivery, with its last line as the reason.

Output past 64 KB is cut off.
