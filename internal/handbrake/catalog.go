package handbrake

var Catalog = map[string]EncoderCaps{
	"x265":       x265Caps("x265"),
	"x265_10bit": x265Caps("x265_10bit"),
	"x265_12bit": x265Caps("x265_12bit"),

	"x264":       x264Caps("x264"),
	"x264_10bit": x264Caps("x264_10bit"),

	"nvenc_h264":       nvencCaps("nvenc_h264", []string{"baseline", "main", "high"}),
	"nvenc_h265":       nvencCaps("nvenc_h265", []string{"main"}),
	"nvenc_h265_10bit": nvencCaps("nvenc_h265_10bit", []string{"main10"}),
	"nvenc_av1":        nvencCaps("nvenc_av1", []string{"main"}),
	"nvenc_av1_10bit":  nvencCaps("nvenc_av1_10bit", []string{"main"}),

	"qsv_h264":       qsvCaps("qsv_h264", []string{"auto", "baseline", "main", "high"}),
	"qsv_h265":       qsvCaps("qsv_h265", []string{"auto", "main"}),
	"qsv_h265_10bit": qsvCaps("qsv_h265_10bit", []string{"auto", "main10"}),
	"qsv_av1":        qsvCaps("qsv_av1", []string{"auto", "main"}),
	"qsv_av1_10bit":  qsvCaps("qsv_av1_10bit", []string{"auto", "main"}),

	"vce_h264":       vceCaps("vce_h264", []string{"auto", "baseline", "main", "high"}),
	"vce_h265":       vceCaps("vce_h265", []string{"auto", "main"}),
	"vce_h265_10bit": vceCaps("vce_h265_10bit", []string{"auto", "main10"}),
	"vce_av1":        vceCaps("vce_av1", []string{"auto", "main"}),

	"vt_h264":       vtCaps("vt_h264", []string{"auto", "baseline", "main", "high"}),
	"vt_h265":       vtCaps("vt_h265", []string{"auto", "main"}),
	"vt_h265_10bit": vtCaps("vt_h265_10bit", []string{"auto", "main10"}),

	"svt_av1":       svtAV1Caps("svt_av1"),
	"svt_av1_10bit": svtAV1Caps("svt_av1_10bit"),

	"mpeg4":     {Name: "mpeg4"},
	"mpeg2":     {Name: "mpeg2"},
	"VP8":       {Name: "VP8"},
	"VP9":       {Name: "VP9"},
	"VP9_10bit": {Name: "VP9_10bit"},
	"theora":    {Name: "theora"},
}

var h26xLevels = []string{
	"auto",
	"1.0", "1b", "1.1", "1.2", "1.3",
	"2.0", "2.1", "2.2",
	"3.0", "3.1", "3.2",
	"4.0", "4.1", "4.2",
	"5.0", "5.1", "5.2",
	"6.0", "6.1", "6.2",
}

func x265Caps(name string) EncoderCaps {
	return EncoderCaps{
		Name:    name,
		Presets: []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"},
		Tunes:   []string{"psnr", "ssim", "grain", "zero-latency", "fast-decode", "animation"},
		Profiles: []string{
			"auto",
			"main", "main-intra", "mainstillpicture",
			"main10", "main10-intra",
			"main12", "main12-intra",
			"main422-10", "main422-10-intra",
			"main422-12", "main422-12-intra",
			"main444-8", "main444-intra", "main444-stillpicture",
			"main444-10", "main444-10-intra",
			"main444-12", "main444-12-intra",
		},
		Levels: h26xLevels,
	}
}

func x264Caps(name string) EncoderCaps {
	return EncoderCaps{
		Name:    name,
		Presets: []string{"ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"},
		Tunes:   []string{"film", "animation", "grain", "stillimage", "psnr", "ssim", "fastdecode", "zerolatency"},
		Profiles: []string{
			"auto", "baseline", "main", "high", "high10", "high422", "high444",
		},
		Levels: h26xLevels,
	}
}

func nvencCaps(name string, profiles []string) EncoderCaps {
	return EncoderCaps{
		Name:     name,
		Presets:  []string{"slowest", "slower", "slow", "medium", "fast", "faster", "fastest"},
		Tunes:    []string{"hq", "ll", "ull", "lossless"},
		Profiles: profiles,
		Levels:   h26xLevels,
	}
}

func qsvCaps(name string, profiles []string) EncoderCaps {
	return EncoderCaps{
		Name:     name,
		Presets:  []string{"veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"},
		Profiles: profiles,
		Levels:   h26xLevels,
	}
}

func vceCaps(name string, profiles []string) EncoderCaps {
	return EncoderCaps{
		Name:     name,
		Presets:  []string{"speed", "balanced", "quality"},
		Profiles: profiles,
		Levels:   h26xLevels,
	}
}

func vtCaps(name string, profiles []string) EncoderCaps {
	return EncoderCaps{
		Name:     name,
		Presets:  []string{"speed", "quality"},
		Profiles: profiles,
		Levels:   h26xLevels,
	}
}

func svtAV1Caps(name string) EncoderCaps {
	presets := make([]string, 14)
	for i := 0; i <= 13; i++ {
		presets[i] = []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13"}[i]
	}
	return EncoderCaps{
		Name:    name,
		Presets: presets,
		Tunes:   []string{"vq", "psnr", "fastdecode"},
	}
}
