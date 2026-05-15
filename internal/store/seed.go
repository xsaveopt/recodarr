package store

import "context"

// seedDefaultProfiles inserts opinionated starter profiles on a fresh install (empty profiles
// table). If the user later deletes them, they don't come back — the gate is "any row exists",
// not "row with this name exists".
func (s *Store) seedDefaultProfiles(ctx context.Context) error {
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, p := range defaultProfiles() {
		if _, err := s.UpsertProfile(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func defaultProfiles() []ProfileRow {
	return []ProfileRow{
		{
			Name:            "Modern anime - x265",
			Encoder:         "x265_10bit",
			EncoderPreset:   "slow",
			EncoderTune:     "animation",
			Quality:         24,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    96,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			ExtraArgs:       "--encopts aq-mode=3:aq-strength=0.8:psy-rd=1.0:psy-rdoq=1.5:deblock=1,1:bframes=8:ref=6:rc-lookahead=60:sao=0",
		},
		{
			Name:            "Live action — x265",
			Encoder:         "x265_10bit",
			EncoderPreset:   "slow",
			Quality:         23,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    128,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			ExtraArgs:       "--encopts aq-mode=3:aq-strength=1.0:psy-rd=1.5:psy-rdoq=1.0:deblock=-1,-1:bframes=6:ref=4:rc-lookahead=40",
		},
		{
			Name:            "Modern anime - nvenc",
			Encoder:         "nvenc_h265_10bit",
			EncoderPreset:   "slowest",
			EncoderTune:     "hq",
			EncoderProfile:  "main10",
			Quality:         28,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    96,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			ExtraArgs:       "--encopts spatial-aq=1:temporal-aq=1:rc-lookahead=32",
		},
		{
			Name:            "Live action — nvenc",
			Encoder:         "nvenc_h265_10bit",
			EncoderPreset:   "slowest",
			EncoderTune:     "hq",
			EncoderProfile:  "main10",
			Quality:         27,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    128,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			ExtraArgs:       "--encopts spatial-aq=1:temporal-aq=1:rc-lookahead=32",
		},

		// --- Intel QSV (HEVC) ---
		// QSV CQ doesn't map 1:1 to x265 CRF — it sits closer to NVENC's CQ
		// scale. CQ 30 here targets visually-near-source on Arc/11th-gen+. Older
		// iGPUs (Kaby/Coffee/Tiger Lake) still work but quality/efficiency is
		// noticeably worse; a value 2–3 lower may be needed. Pre-Kaby Lake
		// can't do HEVC at all.
		{
			Name:            "Modern anime — qsv (HEVC)",
			Encoder:         "qsv_h265_10bit",
			EncoderPreset:   "slower",
			EncoderProfile:  "main10",
			Quality:         32,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    96,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			ExtraArgs:       "--encopts la-depth=40:b-pyramid=1",
		},
		{
			Name:            "Live action — qsv (HEVC)",
			Encoder:         "qsv_h265_10bit",
			EncoderPreset:   "slower",
			EncoderProfile:  "main10",
			Quality:         30,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    128,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			ExtraArgs:       "--encopts la-depth=40:b-pyramid=1",
		},

		// --- Intel QSV (AV1) ---
		// AV1 hardware encode requires Arc (DG2) or Lunar Lake / Battlemage iGPU.
		// Earlier QSV silicon will fail at encode time. AV1 is ~25% more efficient
		// than HEVC at equivalent quality, so CQ runs higher than the HEVC profiles.
		{
			Name:            "Modern anime — qsv (AV1)",
			Encoder:         "qsv_av1_10bit",
			EncoderPreset:   "slower",
			Quality:         34,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    96,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			ExtraArgs:       "--encopts la-depth=40",
		},
		{
			Name:            "Live action — qsv (AV1)",
			Encoder:         "qsv_av1_10bit",
			EncoderPreset:   "slower",
			Quality:         32,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    128,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			ExtraArgs:       "--encopts la-depth=40",
		},

		// --- AV1 software (SVT-AV1) ---
		// SVT-AV1 presets are numeric: 0 = slowest/best (impractical for libraries),
		// 13 = fastest. Preset 4 is the sweet spot for archival quality on a beefy
		// CPU (~10-15 fps on a modern desktop at 1080p); preset 6 is a solid balance
		// for nightly batches. CRF scale is similar to x265 but slightly more lenient
		// — the values below assume the user wants noticeably smaller files than the
		// x265 profiles produce. tune=0 is psnr-optimized (good for animation);
		// tune=1 is subjective-quality (better for live action).
		{
			Name:            "Modern anime — svt-av1",
			Encoder:         "svt_av1_10bit",
			EncoderPreset:   "4",
			Quality:         32,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    96,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			ExtraArgs:       "--encopts tune=0:film-grain=0:enable-overlays=1",
		},
		{
			Name:            "Live action — svt-av1",
			Encoder:         "svt_av1_10bit",
			EncoderPreset:   "5",
			Quality:         30,
			ContainerFormat: "mkv",
			AudioEncoder:    "opus",
			AudioBitrate:    128,
			AudioMixdown:    "stereo",
			SubtitleCopy:    true,
			// film-grain=8 synthesizes grain at decode time so the encoder can
			// throw away the source's noise (huge bitrate savings on grainy
			// live-action sources). enable-overlays improves bitrate efficiency
			// on shot transitions. tune=1 optimizes for visual quality over PSNR.
			ExtraArgs:       "--encopts tune=1:film-grain=8:enable-overlays=1",
		},
	}
}
