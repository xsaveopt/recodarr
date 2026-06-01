package store

import "context"

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

			ExtraArgs: "--encopts tune=1:film-grain=8:enable-overlays=1",
		},
	}
}
