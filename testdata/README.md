# testdata/

Canned HTTP exchange fixtures used by `pkg/shape`, `pkg/creds`,
`pkg/graph`, `pkg/fuzz`, `pkg/download` tests.

The anikage fixtures are hand-written samples of the same shape as
the responses the live anikage.cc API returns. They are not literal
captures (the live slugs/keys change), but they are representative
of the structure the classifier and downstream packages need to
recognize.

Files:

- `anikage_episodes.json` — top-level array of episode objects, used
  to test `KindJSONList` with `id`, `number`, `title`, `slug`,
  `img` fields.
- `anikage_sources.json` — single object with `sources[0].url`
  pointing at a base64-encoded cross-host URL. Tests `KindJSON` with
  `CrossHost` extraction.
- `anikage_error.json` — error envelope with `success: false` and
  `error.message: "No episodes found for provider pahe"`. Tests
  `KindError` and `MissingValues` extraction.
- `hls_master.m3u8` — master playlist with one variant. Tests
  `KindHLSMaster` and `VariantPath` extraction.
- `hls_variant.m3u8` — variant playlist with five segments. Tests
  `KindHLSVariant` and `SegmentCount`.

When you need a new fixture, capture it from the live site with
`curl -i` once, save the relevant slice here, and reference it by
name in the test.
