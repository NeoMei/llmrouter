# Local H.264 fixture

`h264.mp4` is generated test footage, not customer/supplier media. It is a real
playable 1-second, five-frame, 64×48 progressive yuv420p constrained-baseline
H.264/avc1 MP4, no audio, no edit list, faststart, one video track.

Generation (local FFmpeg with libx264; exact version in evidence):

```sh
ffmpeg -hide_banner -f lavfi -i testsrc2=size=64x48:rate=5 -t 1 -an \
  -c:v libx264 -profile:v baseline -pix_fmt yuv420p -use_editlist 0 \
  -movflags +faststart -y service/relaymedia/testdata/h264.mp4
```

SHA-256: `ddd252740e82ca7b8e9215188c2d61a8a2a1c7d539aaef93332fb849aa21fbe1`
Size: 2,859 bytes. Fixture decoded locally to FFmpeg's null sink successfully;
ffprobe's complete stream/format output, generation stderr/version/codec,
command, hash and exits are retained in Task8a evidence. Production does **not**
invoke FFmpeg/ffprobe or claim full decode-quality verification.

The production subset is deliberately narrower than all playable MP4: exactly
one video track, baseline H.264, progressive 4:2:0, one SPS/PPS with ID 0, P/I
slices, 4-byte NAL lengths, identity track matrix, square pixels, no edit list,
no audio, no fragments, no encrypted/external references, version-0 timing,
32-bit ordinary boxes and chunk offsets, no HRD or nonzero composition offsets.
Unsupported containers/tracks/codecs fail closed. The file remains a genuine
playable positive fixture; corruptions in tests are derived negative fixtures.
