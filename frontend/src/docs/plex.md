# Plex

Connect Plex in the [setup guide](/setup), which finds a local server and its token for you, or set `PLEX_MONITOR`, `PLEX_URL` and `PLEX_TOKEN` under **[Settings → Plex](/settings)**.

## The Plex page

**[Media → Plex](/plex)** shows:

- **The server**: up or down, response time, version, and whether an update is available.
- **Now playing**: who is watching what, on which player, and how: direct play, direct stream or transcode, with hardware transcoding and throttling flagged. Also progress, quality, and bandwidth split between local and remote.
- **Charts** of concurrent streams and bandwidth.
- **Libraries** with their item counts.
- **Recently finished** streams.

The Overview's _Now playing_ card shows the current streams too.

## The last week

The Plex page looks back over the last 7 or 30 days: how many streams and hours were watched, who watched most, the most played shows and films, which days were busiest, the most streams at once, and how many were transcoded or watched from outside the home network. Below that is what Plex's own history says was finished, which also covers anything that played while Nightjar was not running, and what was added to each library.

Each transcode says why on the Plex page and in its stream's details, with what would let it play as it is: subtitles burned in (pick a text subtitle), quality lowered (raise the player's quality, or the remote limit), a video or audio codec the player cannot play, or just repackaging. The look back counts the reasons too.

Nightjar keeps every stream it sees finish, for as long as `HISTORY_RETENTION_DAYS`. A stream left paused counts for no more than the length of what was playing. The same numbers go into the [weekly report](/docs/incidents#the-weekly-report).

## Managing a stream

Select a stream under _Now playing_, or its **Manage** button, to open it up close:

- **Who and where**: the user, the player and app, the platform, and whether it plays on your network or remotely, with the player's IP address.
- **How it is delivered**: direct play, direct stream or transcode, and for a transcode whether the GPU does it, what it re-encodes (video, audio or both) and how far ahead of playback it is.
- **Quality and bandwidth**: the source quality and bitrate, and what the stream uses now.

**Stop stream** ends playback and shows a message on the player: pick one of the ready-made ones or write your own, up to 200 characters. Leave it empty and the viewer sees _The server owner stopped this stream_. The dashboard asks once more before it stops anything.

> [!NOTE]
> Plex lets other apps stop a stream only with **Plex Pass** on the server owner's account, and with the owner's token in `PLEX_TOKEN`. Without them Plex refuses, and the dashboard says so. A stopped stream moves to _Recently finished_, and the monitor's log records who stopped what.

Plex has no way for another app to pause a stream or change its quality, so stopping is the one control there is.

## Alerts

- Plex goes down after more than `PLEX_RETRIES` failed checks, and comes back.
- `PLEX_TRANSCODE_ALERT` streams are transcoding at once (0 turns this off).
- Optionally every new stream, with `PLEX_NOTIFY_STREAMS`.

They go to `PLEX_WEBHOOK` if you set one, else to the main Discord webhook.

## Cleanup

**Media → [Cleanup](/plex/cleanup)** removes movies and shows nobody watches, to free space. It is off until you turn it on under **Settings → Media cleanup**; until then the page shows what it would list.

1. **Never watched.** A movie or show qualifies when no one on the server has ever played it, `MEDIA_CLEANUP_DAYS` (60) days after Plex added it. Nightjar reads Plex's whole watch history for every account, and counts any stream it saw, even one stopped partway. A show with one episode watched stays. If the history cannot be read, nothing is listed.
2. **Leaving soon.** It is listed for `MEDIA_CLEANUP_NOTICE_DAYS` (7) days, and Discord is told what is leaving and when. Press **Keep** to spare it for good; if someone starts watching it, it comes off the list by itself.
3. **Seeded first.** Its torrents (the ones Sonarr or Radarr imported it from, or for anything else a torrent with its title and year) keep seeding until they reach `MEDIA_CLEANUP_RATIO` (1.0). After `MEDIA_CLEANUP_RATIO_DAYS` (30) more days it goes anyway.
4. **Removed.** The torrents are deleted with their files, then the media through Radarr or Sonarr with its files, which also stops them adding it again. Anything they do not manage is deleted through Plex, which needs **Allow media deletion** turned on in Plex. Discord is told what was removed and how much space it freed.

Nightjar finds Sonarr and Radarr on this machine by their `config.xml` and says so on the page. To use others, set `SONARR_URL`, `SONARR_API_KEY`, `RADARR_URL` and `RADARR_API_KEY` under **Settings → Media cleanup**; the keys are under Settings → General in each app.

When the disk a title is on passes `MEDIA_CLEANUP_DISK_FULL` (90%), what is leaving from it goes after a day instead of the week, biggest first. `MEDIA_CLEANUP_LIBRARIES` limits it to some libraries. Nightjar checks every six hours, or now with **Check now**, and removes at most ten titles a pass.
