# im-local specification

im-local is an HTTP image transformation service. Transformations are expressed
as a chain in the query string, the output format is negotiated (including
JPEG XL), and background removal runs with a locally executed segmentation
model.

This document specifies the observable behaviour of the service. `README.md`
covers usage and setup.

---

## 1. Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/process` | Fetch a pristine image, apply transformations, encode, and return the derivative. |
| GET | `/doc` | Documentation page with a live example per feature and a playground. |
| GET | `/sample.jpg` | Built-in sample image, embedded in the binary. |
| GET | `/sample-studio.jpg` | Built-in sample with a flat backdrop. |
| POST | `/upload` | Store a pristine image so it can be used as a source. |
| GET | `/uploads/:name` | Serve a stored upload. |
| GET | `/health` | Liveness probe, returns `{"status":"ok"}`. |

### 1.1 `GET /process`

| Parameter | Required | Description |
| --- | --- | --- |
| `url` | yes | Absolute `http` or `https` URL of the pristine image. |
| `im` | no | Transformation chain. |
| `imop` | no | Alias for `im`. Takes precedence when both are present. |
| `width`, `height` | no | Legacy shortcut, used only when neither `im` nor `imop` is present. Equivalent to `Resize,width=..,height=..`. |
| `out` | no | Output format. See section 4. |
| `prefer` | no | Preference order for negotiation, for example `avif,webp,jxl`. |
| `quality` | no | Integer 1-100, default 90. Applies to JPEG and JPEG XL. |
| `nocache` | no | Any non-empty value skips the cache read. The result is still stored. |

`url`, `im`, `imop`, `out`, `prefer`, `quality`, and `nocache` are read from the raw query
string rather than through `url.ParseQuery`, because Go rejects query strings
containing `;`, which is the chain separator used here.

Status codes:

| Status | Condition |
| --- | --- |
| 200 | Derivative returned. |
| 304 | `If-None-Match` matches the current `ETag`. |
| 400 | Missing `url`, invalid `quality`, unknown `out` or `prefer` entry, disallowed source host, unknown transformation, or invalid transformation arguments. |
| 502 | The pristine image could not be fetched or decoded. |
| 500 | Encoding failed. |

Errors are JSON: `{"error": "<message>"}`.

### 1.2 `POST /upload`

Multipart form with an `image` field, at most 32 MiB. The payload must decode as
JPEG, PNG, GIF, WebP, or JPEG XL. The file is stored under the MD5 of its
content, and the response is:

```json
{
  "url": "http://host/uploads/<md5>.<ext>",
  "name": "<original file name>",
  "fileName": "<md5>.<ext>",
  "format": "png",
  "width": 640,
  "height": 848,
  "bytes": 867740
}
```

`url` is absolute, because `/process` fetches sources over HTTP.

---

## 2. Transformation language

### 2.1 Grammar

```
chain       := transform ( ";" transform )*
transform   := name [ "=" value ] ( "," argument )*
argument    := key "=" value | flag
value       := literal | "(" literal ( "," literal )* ")" | "$(" chain ")"
```

- Transformations are applied left to right.
- Names and argument keys are matched case-insensitively, ignoring `-`, `_`,
  spaces, and `/`.
- `;` and `,` inside parentheses do not split.
- A bare word in the argument list is a flag, for example `allowExpansion`.
- `$(...)` holds a nested chain, used by the conditional transformations.
- `Name=(a,b)` is a shortcut for the first two numeric parameters of the
  transformation. `Name=value` is a shortcut for the single primary parameter.
- An unknown transformation is an error (HTTP 400). Unknown arguments are
  ignored.

### 2.2 Transformations

Dimensions accept `width` and `height` arguments, the `size=(w,h)` shortcut, or
the `Name=(w,h)` shortcut. When only one dimension is given, the other follows
from the aspect ratio.

| Transformation | Arguments | Behaviour |
| --- | --- | --- |
| `Resize` | `width`, `height` | Scales to the given size, Lanczos. |
| `Scale` | `width`, `height` (factors, default 1) | Scales relative to the current size. |
| `FitAndFill` | `width`, `height` | Fits inside the box, fills the remainder with a blurred, filled copy. |
| `Crop` | `width`, `height`, `xPosition`/`x`, `yPosition`/`y`, `rect=(x,y,w,h)`, `gravity`, flag `allowExpansion` | Cuts out a rectangle. `gravity` overrides the position. `allowExpansion` pads with transparency instead of clamping to the image. |
| `AspectCrop` | `width`, `height` (ratio parts), `xPosition`, `yPosition` (0-1, default 0.5), flag `allowExpansion` | Crops to the aspect ratio, or expands with transparent pixels. One original dimension is always preserved. |
| `RelativeCrop` | `north`, `south`, `east`, `west` | Shrinks or expands each edge. Expanded area is transparent. |
| `RegionOfInterestCrop` | `width`, `height`, `regionOfInterest=(x,y)` or `regionOfInterest=(anchor=(x=..,y=..),..)` | Crops around a focal point, clamped to the image. |
| `FeatureCrop` | `width`, `height` | Crops around the image centre after fitting the target ratio. |
| `FaceCrop` | `width`, `height` | Detects faces with the pigo cascade and crops around the strongest one. Falls back to `FeatureCrop`. |
| `SmartCrop` | `width`, `height` | `FaceCrop` when a face is found, `FeatureCrop` otherwise. |
| `Trim` | `tolerance` (default 0.02) | Trims uniform edges, measured against the top-left pixel. |
| `Rotate` | `degrees` | Rotates clockwise for positive values, expanding the canvas with transparency. |
| `Mirror` | flag `horizontal` or `vertical`, or `Mirror=vertical` | Flips the image. Defaults to horizontal. |
| `Shear` | `x`, `y` (also `horizontal`, `vertical`) | Slants the image, expanding the canvas with transparency. |
| `Goop` | `chaos` (0-1, default 0.5) | Sinusoidal distortion. |
| `Blur` | `sigma` (also `strength`, `blur`), or `Blur=2` | Gaussian blur. |
| `UnsharpMask` | `gain` (default 1) | Sharpening. |
| `Contrast` | `contrast` (default 1) | 1 leaves the image unchanged. |
| `Grayscale` | none | Converts to gray. |
| `HSL` | `hue` (turns), `saturation`, `lightness` (multipliers) | Adjusts colours in HSL. |
| `HSV` | `hue` (turns), `saturation`, `value` | Adjusts colours in HSV. |
| `MonoHue` | `hue` (degrees, default 0) | Sets one hue, preserving saturation and lightness. |
| `MaxColors` | `colors` (2-256) | Uniform palette reduction per channel. |
| `BackgroundColor` | `color` (RGB or RGBA hex), or `BackgroundColor=00ff00` | Composites the image over a solid colour. |
| `Opacity` | `opacity`, or `Opacity=0.5` | Multiplies the alpha channel. |
| `ChromaKey` | `hue`, `hueTolerance`, `hueFeather`, `saturationTolerance`, `saturationFeather`, `lightnessTolerance`, `lightnessFeather` | Makes a hue range transparent. Defaults target a green screen. |
| `RemoveColor` | `color`, `tolerance` (0.2), `feather` (0) | Makes one colour transparent, with a feathered edge. |
| `BackgroundRemove` | see section 5 | Removes the background. Alias `RemoveBackground`. |
| `Composite` | `image=(url=..)`, `gravity`, `xPosition`, `yPosition`, `scale`, `opacity`, `placement=over\|under` | Overlays or underlays a remote image. |
| `Append` | `image=(url=..)`, `gravity` (`East` default, `South`/`North` for vertical) | Places a remote image beside this one. |
| `IfDimension` | `value`, `dimension=width\|height`, `greaterThan`, `lessThan`, `equal`, `default` | Applies the matching nested chain. |
| `IfOrientation` | `portrait`, `landscape`, `square`, `default` | Applies the matching nested chain. |

Gravity values: `Center`, `North`, `South`, `East`, `West`, `NorthEast`,
`NorthWest`, `SouthEast`, `SouthWest`, plus the aliases `top`, `bottom`, `left`,
`right`, `topright`, `bottomleft`, `bottomright`.

---

## 3. Source images

`url` must use `http` or `https`. Network sources are disabled when
`IM_ALLOWED_HOSTS` is empty, which is the default. When enabled, the hostname
must match that list and the path must match `IM_ALLOWED_PATH_PREFIXES` when the
path list is non-empty. Credentials, unsafe path traversal, and non-standard
public ports are rejected.

Every redirect is revalidated. DNS is resolved by the service, and connections
are pinned to a checked address to prevent DNS rebinding. Loopback, private,
link-local, multicast, and unspecified addresses are rejected unless
`IM_ALLOW_PRIVATE_NETWORKS=true`.

At most `IM_MAX_IMAGE_BYTES` (16 MiB by default) is read. DecodeConfig must
report no more than 20 million pixels or 8192 pixels along either axis before a
full decode occurs. Supported input formats are JPEG, PNG, GIF, WebP, and JPEG
XL. `Composite` and `Append` fetch overlays with the same rules.

---

## 4. Output format

Writable formats are JPEG, PNG, WebP, AVIF, and JPEG XL.

| `out` | Result | Explicit |
| --- | --- | --- |
| absent, `source`, `original` | The source format, limited to a writable one; anything else becomes JPEG. If `prefer` is set, negotiation runs instead. | no |
| `jpeg`, `jpg`, `png`, `webp`, `avif`, `jxl` | That format. | yes |
| `auto` | Negotiated. | only when a format was negotiated |
| anything else | HTTP 400. | - |

Negotiation walks the preference list, `prefer` when given and `avif,webp,jxl`
otherwise, and returns the first format the client accepts. If none match, the
source format is used.

A format counts as accepted when `Accept` contains its exact media range with a
non-zero `q`. Wildcards such as `image/*` and `*/*` do not count, because
browsers send them for formats they cannot decode.

**Transparency rule.** If the format was not explicit and the processed image
has an alpha channel that the format cannot store, PNG is used instead.

**Quality.** `quality` is 1-100, default 90. JPEG, WebP, and JPEG XL take it
directly; AVIF uses 75% of it, since its scale produces much larger files at the
same number. AVIF encodes at speed 10.

Every response carries `Vary: Accept`.

Encoders call a shared `libavif`, `libwebp`, or `libjxl` when the library can be
loaded by plain name, and otherwise run the same library compiled to WASM.

## 5. Background removal

`BackgroundRemove` has two implementations.

### 5.1 Model method (default)

| Argument | Default | Description |
| --- | --- | --- |
| `model` | `isnet-general-use` | Also `u2net`, `silueta`, `u2netp`. |
| `threshold` | `0` | Above 0, the soft mask is hardened around this coverage. |
| `feather` | `0.1` | Width of the ramp around `threshold`. |
| `refine` | `1` | Guided-filter edge refinement, `0` disables it. |
| `decontaminate` | `1` | Foreground colour estimation on semi-transparent pixels, `0` disables it. |
| `method` | `model` | `color` selects the colour method. |
| `fallback` | none | `color` falls back to the colour method when the model is unavailable. |

Pipeline:

1. The image is letterboxed into a square of the model input size, preserving
   the aspect ratio, and normalised with the model's mean and standard
   deviation.
2. onnxruntime runs the model. The first output channel is min-max normalised,
   the letterbox is removed, and the mask is scaled to the source size.
3. With `refine=1`, a guided filter using image luminance as the guide pulls the
   mask edges onto the image edges.
4. The mask is remapped: without a threshold the range [0.02, 0.98] is stretched
   to [0, 1]; with one, the ramp is `threshold ± feather/2`.
5. With `decontaminate=1`, partially transparent pixels are unmixed as
   `F = (I - (1-a)B)/a`, where `B` is a background-weighted local average. This
   removes the halo of the original background.
6. The mask becomes the alpha channel.

Both the onnxruntime shared library and the model weights are resolved lazily,
so the service starts without them. When they are missing the transformation
fails with HTTP 400 and an actionable message, unless `fallback=color` is set.

| Variable | Purpose |
| --- | --- |
| `ONNXRUNTIME_SHARED_LIBRARY` | Path to the onnxruntime shared library. Otherwise the usual install locations are searched. |
| `IM_SEGMENTATION_DOWNLOAD=0` | Disables weight downloads. |
| `IM_SEGMENTATION_MODEL_<NAME>` | Path to an existing model file. `<NAME>` is the model name without punctuation, for example `IM_SEGMENTATION_MODEL_ISNETGENERALUSE`. |

Weights are downloaded once into the model directory from the rembg release
assets.

### 5.2 Colour method

`method=color` needs no model. Arguments: `tolerance` (0.12), `feather` (0.08),
`colors` (4), `margin` (2), `minSupport` (0.25).

The border pixels are clustered with k-means. Clusters holding at least
`minSupport` of the border samples form the background model; if none qualify,
the largest cluster is used. Each pixel's alpha is derived from its distance to
the nearest background colour, and only pixels connected to the border through
background-coloured pixels are made transparent, so enclosed areas of the same
colour inside the subject remain opaque.

---

## 6. Caching

Derivatives are stored in the cache directory, one file per derivative, written
to a temporary file and renamed into place.

The cache key is the MD5 of:

```
url=<source url> \0 im=<chain> \0 out=<format> \0 explicit=<bool> \0 quality=<n>
```

Consequences:

- Query parameter order does not matter.
- Parameters that do not influence the output do not split entries.
- A negotiated format and an explicitly requested one never share an entry,
  because the negotiated one may switch to PNG.

An entry is used when its modification time is within the cache TTL, 24 hours by
default. `nocache=1` skips the read and refreshes the entry.

Response headers:

| Header | Meaning |
| --- | --- |
| `ETag` | MD5 of the response body. A matching `If-None-Match` yields 304. |
| `Cache-Control` | `public, max-age=<ttl>`. |
| `Vary` | `Accept`. |
| `X-Cache` | `HIT` or `MISS`. |
| `X-Process-Time-Ms` | Server-side duration. |
| `X-Image-Width`, `X-Image-Height` | Dimensions of the derivative. |

`Content-Type` of a cache hit is derived from the stored bytes, not from the
negotiation.

---

## 7. Configuration

All settings come from the environment.

| Variable | Default | Meaning |
| --- | --- | --- |
| `IM_PORT` | `8080` | Listen port. |
| `IM_ALLOWED_HOSTS` | empty | Permitted source hosts; empty denies all. |
| `IM_ALLOWED_PATH_PREFIXES` | empty | Optional source-path allowlist. |
| `IM_ALLOW_PRIVATE_NETWORKS` | `false` | Allow private/non-routable addresses and non-standard ports. |
| `IM_INSECURE_TLS` | `false` | Skip source TLS verification. |
| `IM_MAX_IMAGE_BYTES` | `16777216` | Maximum compressed source bytes. |
| `IM_MAX_IMAGE_PIXELS` | `20000000` | Maximum decoded source pixels. |
| `IM_MAX_OUTPUT_PIXELS` | `25000000` | Maximum derivative pixels. |
| `IM_MAX_DIMENSION` | `8192` | Maximum width or height. |
| `IM_MAX_TRANSFORMATIONS` | `20` | Maximum operations including nested branches. |
| `IM_MAX_QUERY_BYTES` | `16384` | Maximum raw query bytes. |
| `IM_MAX_CONCURRENT` | `4` | Processing slots per process. |
| `IM_QUEUE_TIMEOUT` | `5s` | Wait before an excess request returns 503. |
| `IM_UPLOADS_ENABLED` | `false` | Enable uploads. |
| `IM_UPLOAD_TOKEN` | empty | Optional bearer token for uploads. |
| `IM_MAX_UPLOAD_BYTES` | `10485760` | Maximum compressed upload bytes. |
| `IM_UPLOAD_STORAGE_BYTES` | `536870912` | Upload quota. Oldest files are removed first. |
| `IM_UPLOAD_TTL` | `1h` | Upload lifetime. |
| `IM_CACHE_STORAGE_BYTES` | `5368709120` | Derivative-cache quota. |
| `IM_CACHE_DIR` | `./cache` | Derivative cache. |
| `IM_MODEL_DIR` | `./models` | Segmentation weights. |
| `IM_UPLOAD_DIR` | `./uploads` | Uploaded images. |
| `IM_CACHE_TTL` | `24h` | Cache lifetime. |
| `DEMO_MODE` | `false` | Clean cache/upload artifacts older than 90 minutes every 90 minutes. |
| `ONNXRUNTIME_SHARED_LIBRARY` | searched | onnxruntime library path. |
| `IM_SEGMENTATION_DOWNLOAD` | `1` | `0` disables model downloads. |
| `IM_SEGMENTATION_MODEL_<NAME>` | none | Existing model path. |

Running with `-healthcheck` probes `/health` on the configured port and exits
with 0 or 1. The HTTP server has read-header, read, write, idle, and graceful
shutdown timeouts.

## 8. Security notes

- Network fetching and uploads are disabled by default.
- Redirect validation, DNS pinning, private-address rejection, source-byte and
  pixel limits, output geometry limits, bounded transformation chains, and a
  processing semaphore are enforced by the application.
- Upload and derivative directories have byte quotas. Uploads expire; demo mode
  also removes cache/upload artifacts older than 90 minutes on a 90-minute
  schedule. Segmentation models are retained.
- `IM_ALLOWED_HOSTS=*` still allows every public origin. Combining it with
  `IM_ALLOW_PRIVATE_NETWORKS=true` recreates a broad SSRF surface.
- An allowed self-host must use `IM_ALLOWED_PATH_PREFIXES` to exclude `/process`
  and prevent recursive source requests.
- Public deployments still require edge rate limiting and storage/resource
  limits. `deploy/kubernetes.yaml` supplies ingress limits, pod bounds, and a
  private-network-denying egress policy for the public demo.
