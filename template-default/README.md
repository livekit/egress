# Default LiveKit Recording Templates

## Using our templates

We currently have 4 templates available - `speaker-light`, `speaker-dark`, `grid-light`, and `grid-dark`.
The `speaker` templates will show the current active speaker taking up most of the screen, with other participants in the side bar.
The `grid` templates will show a 1x1, 2x2, or 3x3 grid for up to 1, 4, or 9 participants respectively.

Our templates are deployed at https://egress-composite.livekit.io.

## Building your own templates

When using this option, you must handle room connection - the recorder will only open the url and start recording.
It's easiest to start with a copy of one of our existing templates.

### Custom template requirements

2. The template **should** use `layout` names as paths (see [src/App.tsx](https://github.com/livekit/egress/blob/main/web/src/App.tsx)).
3. The template **should** take `url` and `token` as query parameters. `url` refers to your LiveKit server websocket url (`ws_url` in the recorder's `config.yaml`).
4. The template **should** use the `url` and `token` parameters to connect to your LiveKit room
   ([src/common.ts:useParams](https://github.com/livekit/egress/blob/main/web/src/common.ts#L37)).
5. The template **must** `console.log('START_RECORDING')` to start the recording.
   1. If your template does not log `START_RECORDING`, the recording will not start.
6. The template **should** `console.log('END_RECORDING')` to stop the recording.
   1. If your template does not log `END_RECORDING`, the recording will need to be stopped manually by sending a
      `StopEgressRequest` to your LiveKit server.
   2. See [src/common.ts:onConnected](https://github.com/livekit/egress/blob/main/web/src/common.ts#L13)
      for recommended `START_RECORDING` and `END_RECORDING` implementation.

### Using your template

First, you must deploy your template(s).

Once your template is deployed, update your recorder's `config.yaml` and add
```yaml
api_key: your-livekit-server-api-key
api_secret: your-livekit-server-api-secret
ws_url: wss://your-livekit-server-address.com
template_base: https://your-template-address.com
```
* Note: the hash is necessary if using hash routing, which is what our templates use. For example, the default
  `template_address` is `https://egress-composite.livekit.io`.
* If you want to use both your own templates and LiveKit templates, you can override the template address per
  request using the `RoomCompositeRequest.CustomBaseUrl` field.

Send a request to your recorder using your layout name in the `RoomCompositeRequest.Layout` field.

The recorder will generate a `token` to join the room, then build the url
`{config.template_address}?layout={request.layout}&url={encoded config.ws_url}&token={token}`.

In this example, the generated address would look like
`https://your-template-address.com?layout=my-layout&url=wss%3A%2F%2Fyour-livekit-server-address.com&token=...`
