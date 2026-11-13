# probe-slack

A [Probe](https://github.com/mozership/probe) action that posts a message to a Slack channel: the notes of a run, a text the workflow writes, or both.

Actions can leave notes for people besides whether their step passed, and a later step reads them as [`notes`](https://probe.linyo.ws/reference/yaml-configuration#notes), with the job and the step of each and the secrets hidden. Probe does not post them anywhere. This action posts them to Slack, so that a team hears of a critical note, or of a failed run on a schedule, where it talks. Posting a message is all it does with Slack.

It needs Probe v1.32.0 or later, the first with the `notes` context.

```yaml
name: Nightly
actions:
  slack: github.com/mozership/probe-slack@<commit SHA>
secrets:
  - SLACK_WEBHOOK_URL
vars:
  slack_webhook: "{{SLACK_WEBHOOK_URL}}"
jobs:
- name: Checkout
  id: checkout
  steps:
  # Steps whose actions leave notes.
  - include: checkout-steps.yml

- name: Tell the team
  needs: [checkout]
  always: true
  steps:
  - name: Post the critical notes
    uses: slack
    with:
      webhook_url: "{{vars.slack_webhook}}"
      text: Critical notes of the nightly run
      notes: "{{ filter(notes, .level == 'critical') }}"
```

The last job [`needs`](https://probe.linyo.ws/reference/yaml-configuration#needs) the others with [`always`](https://probe.linyo.ws/reference/yaml-configuration#always), so that it runs once they are over, however they ended, and reads the notes of them all.

The workflow gives the action a name once, under `actions`, and its steps use the name. Probe only takes a full 40-character commit SHA there. The notes of each [release](https://github.com/mozership/probe-slack/releases) start with the action and its commit to copy.

## Two ways to post

| | `webhook_url` | `token` and `channel` |
|---|---|---|
| What it is | An [Incoming Webhook](https://api.slack.com/messaging/webhooks) | A bot token with `chat:write`, for [`chat.postMessage`](https://api.slack.com/methods/chat.postMessage) |
| The channel | The one the webhook was made for | `channel`, by id or `#name`, of those the bot was invited to |
| What it returns | `res.posted` | `res.posted`, and `res.channel` and `res.ts` of the message |

A webhook needs nothing else, and is the way for one channel. A token posts to the channel each step names. Give one of the two, not both, and list the URL of the webhook, or the token, under `secrets`: the URL of a webhook is all it takes to post to it.

Each run posts a new message. The action does not edit one, nor reply in a thread: a message kept up to date for each push belongs on the pull request, with [probe-github-notes](https://github.com/mozership/probe-github-notes).

## Parameters

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `webhook_url` | String | One of the two ways | - | The URL of an Incoming Webhook |
| `token` | String | One of the two ways | - | A bot token with `chat:write` |
| `channel` | String | With `token` | - | The channel, by id such as `C0123456789` or as `#name` |
| `text` | String | `text` or `notes` | - | Markdown written above the notes, or alone |
| `notes` | Array | `text` or `notes` | - | Entries of the `notes` context, such as `"{{ notes }}"` |
| `api_url` | String | No | `https://slack.com/api` | The Web API, for `token` |
| `timeout` | Duration | No | `30s` | Time limit for the step, as `10s` or a number of seconds |

A key that is none of these fails the step, and `probe check` reports it with its line.

When `notes` is empty and there is no `text`, nothing is posted, and `res.posted` is false: a message each night saying a run left no notes is noise. A `text` with no notes is posted alone, such as to say that a run on a schedule failed.

`notes` takes entries as the `notes` context has them; an entry without a `level` of `info`, `warning` or `critical`, or without a `body`, fails the step. Give it the context, or a part of it, as `filter(notes, .level == 'critical')`, rather than a list written by hand.

## What it posts

The message is made of Block Kit [`markdown`](https://api.slack.com/reference/block-kit/blocks#markdown) blocks, so that the Markdown of a note shows as written: the `text`, a line counting the notes of each level, and a block for each note with an emoji of its level, its title, its body, and a line naming its job, its step, the status of the step and, for a job with `repeat`, the run:

```markdown
Critical notes of the nightly run

**The run left 1 note: 1 critical.**

🚨 **critical · The checkout button is under the cookie banner**

On a 390px wide screen the banner covers the button until it is closed.

_Checkout › A first-time shopper · passed_
```

The plain text of the message, which Slack shows in a notification, is the `text` and the line counting the notes.

A value Probe hides is `<secret:NAME>` or `<redacted>` in a note, which Slack would take for a link or a mention, so the action puts it in code, where it shows as it is. Slack takes at most 50 blocks, and 12000 characters in all the `markdown` blocks of a message, so the last notes of a longer message are left out, and a block says how many. A `text` too long to fit even without the notes fails the step.

## Result

| Field | Type | Description |
|---|---|---|
| `res.posted` | Boolean | Whether a message was posted |
| `res.channel` | String | The channel of the message, with `token`; empty with a webhook |
| `res.ts` | String | The timestamp of the message, which names it in the Web API, with `token`; empty with a webhook |
| `res.code` | Integer | The status code of the response, `0` when nothing was posted |
| `req` | Object | `via` (`webhook` or `token`), `channel` and `api_url` with a token, `text` and the number of `notes`; neither the URL of the webhook nor the token |
| `rt` | Duration | How long the step took |
| `status` | Integer | `0` |

An error of Slack fails the step with what Slack answered: `channel_not_found` or `not_in_channel` of `chat.postMessage`, a 404 `no_service` of a webhook that was removed, or a 429 with the seconds Slack asks to wait. So does a post that Slack redirects with a 301 or a 302, which would be sent again as a GET. The URL of a webhook is not told in an error.

The action does not return the notes it posts as `res.notes`, so that they are not left again for the steps after it.

## Guard

The action keeps to the guard of the run, and its `action.yml` declares `guard: [read-only, allow-host]`, so Probe runs it under `--read-only` and `--allow-host` without `--allow-action`.

- Under `--read-only`, a step is refused before any request, as posting writes.
- Under `--allow-host`, the host of `webhook_url` or `api_url`, and of each redirect, must be one the run allows: `hooks.slack.com` for a webhook, and `slack.com` for a token. A URL without a port is taken at the port of its scheme.

A refused step fails with the kind `refused`.

## Testing

`go test ./...` runs against a stand-in for Slack, in `internal/fakeslack`, which refuses what Slack refuses of the blocks of a message. `e2e/workflow.yml` runs the action through Probe: one job leaves notes, with [`hello`](https://probe.linyo.ws/reference/actions/hello), and another posts them to the stand-in served by `e2e/server`:

```sh
go run ./e2e/server &
go build -o e2e/action/probe-slack . && SHOP_PASSWORD=hunter2-e2e probe e2e/workflow.yml
```

`FAKE_SLACK_URL` names the server when it listens elsewhere than on `127.0.0.1:18083`. `e2e/guard.yml` is run under `--read-only` and under `--allow-host`, where its post is refused.

## Releasing

Pushing a `v*` tag builds the executables with GoReleaser and publishes them on the release. The workflow then has `probe manifest` write an `action.yml` with their URLs and SHA-256 digests, commits it to `main`, and adds the commit to the release notes. The name, the description, `guard` and `params` are those of the `action.yml` at the tag, so that file is where to change them. That commit is the one to pin: Probe reads `action.yml` at the pinned commit and refuses an executable whose digest differs.

## License

MIT
