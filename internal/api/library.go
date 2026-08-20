package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xsaveopt/recodarr/internal/arr"
	"github.com/xsaveopt/recodarr/internal/store"
)

const (
	libraryCacheTTL   = 10 * time.Minute
	libraryDeepWorker = 6
)

type libraryItemDTO struct {
	InstanceID     int64    `json:"instanceId"`
	InstanceName   string   `json:"instanceName"`
	ItemID         int64    `json:"itemId"`
	Title          string   `json:"title"`
	Year           int      `json:"year"`
	Path           string   `json:"path"`
	FileCount      int      `json:"fileCount"`
	TotalSize      int64    `json:"totalSize"`
	RuntimeSeconds int      `json:"runtimeSeconds"`
	BitrateBps     int64    `json:"bitrateBps"`
	BitrateExact   bool     `json:"bitrateExact"`
	VideoCodec     string   `json:"videoCodec"`
	Resolution     string   `json:"resolution"`
	TagLabels      []string `json:"tagLabels"`
	Mapped         bool     `json:"mapped"`
	MappedTags     []string `json:"mappedTags"`
}

type libraryMappableTagDTO struct {
	TagID       int64  `json:"tagId"`
	TagLabel    string `json:"tagLabel"`
	ProfileID   int64  `json:"profileId"`
	ProfileName string `json:"profileName"`
}

type libraryInstanceDTO struct {
	ID           int64                   `json:"id"`
	Name         string                  `json:"name"`
	Error        string                  `json:"error,omitempty"`
	TagCount     int                     `json:"tagCount"`
	MappableTags []libraryMappableTagDTO `json:"mappableTags"`
}

type libraryScanDTO struct {
	Kind      string               `json:"kind"`
	Deep      bool                 `json:"deep"`
	ScannedAt time.Time            `json:"scannedAt"`
	Instances []libraryInstanceDTO `json:"instances"`
	Items     []libraryItemDTO     `json:"items"`
}

type libraryFileDTO struct {
	FileID         int64  `json:"fileId"`
	Path           string `json:"path"`
	RelativePath   string `json:"relativePath"`
	Size           int64  `json:"size"`
	RuntimeSeconds int    `json:"runtimeSeconds"`
	BitrateBps     int64  `json:"bitrateBps"`
	BitrateExact   bool   `json:"bitrateExact"`
	VideoBitrate   int64  `json:"videoBitrate"`
	AudioBitrate   int64  `json:"audioBitrate"`
	VideoCodec     string `json:"videoCodec"`
	AudioCodec     string `json:"audioCodec"`
	Resolution     string `json:"resolution"`
	Quality        string `json:"quality"`
}

type libraryCache struct {
	mu      sync.Mutex
	entries map[string]libraryScanDTO
}

func newLibraryCache() *libraryCache {
	return &libraryCache{entries: make(map[string]libraryScanDTO)}
}

func (c *libraryCache) get(key string) (libraryScanDTO, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.ScannedAt) > libraryCacheTTL {
		return libraryScanDTO{}, false
	}
	return e, true
}

func (c *libraryCache) put(key string, v libraryScanDTO) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = v
}

func (c *libraryCache) markTagged(kind string, instanceID, itemID int64, label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range []string{kind, kind + ":deep"} {
		entry, ok := c.entries[key]
		if !ok {
			continue
		}
		for i := range entry.Items {
			it := &entry.Items[i]
			if it.InstanceID != instanceID || it.ItemID != itemID {
				continue
			}
			it.Mapped = true
			if !slices.Contains(it.TagLabels, label) {
				it.TagLabels = append(it.TagLabels, label)
				sort.Strings(it.TagLabels)
			}
			if !slices.Contains(it.MappedTags, label) {
				it.MappedTags = append(it.MappedTags, label)
				sort.Strings(it.MappedTags)
			}
		}
	}
}

func registerLibraryRoutes(r chi.Router, st *store.Store) {
	cache := newLibraryCache()
	r.Get("/library/{kind}", libraryScan(st, cache))
	r.Get("/library/{kind}/{instanceId}/{itemId}/files", libraryFiles(st))
	r.Post("/library/{kind}/{instanceId}/{itemId}/tags", libraryAddTag(st, cache))
}

func parseArrKind(s string) (string, bool) {
	switch s {
	case "sonarr", "radarr":
		return s, true
	default:
		return "", false
	}
}

func libraryScan(st *store.Store, cache *libraryCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind, ok := parseArrKind(chi.URLParam(r, "kind"))
		if !ok {
			http.Error(w, "unknown kind", http.StatusBadRequest)
			return
		}
		deep := r.URL.Query().Get("deep") == "true"
		key := kind
		if deep {
			key += ":deep"
		}
		if r.URL.Query().Get("refresh") != "true" {
			if hit, ok := cache.get(key); ok {
				writeJSON(w, http.StatusOK, hit)
				return
			}
		}

		instances, err := st.ListArrInstances(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mappings, err := st.ListTagMappingsByKind(r.Context(), kind)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mapped := make(map[int64]store.TagMappingRow, len(mappings))
		for _, mp := range mappings {
			mapped[mp.TagID] = mp
		}
		profiles, err := st.ListProfiles(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		profileNames := make(map[int64]string, len(profiles))
		for _, p := range profiles {
			profileNames[p.ID] = p.Name
		}

		out := libraryScanDTO{
			Kind:      kind,
			Deep:      deep,
			ScannedAt: time.Now().UTC(),
			Instances: []libraryInstanceDTO{},
			Items:     []libraryItemDTO{},
		}
		for _, inst := range instances {
			if !inst.Enabled || inst.Kind != kind {
				continue
			}
			entry := libraryInstanceDTO{ID: inst.ID, Name: inst.Name, MappableTags: []libraryMappableTagDTO{}}
			client := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey)
			items, err := client.Library(r.Context())
			if err != nil {
				entry.Error = err.Error()
				out.Instances = append(out.Instances, entry)
				continue
			}
			labels := map[int64]string{}
			if tags, err := client.Tags(r.Context()); err == nil {
				entry.TagCount = len(tags)
				for _, t := range tags {
					labels[t.ID] = t.Label
					mp, ok := mapped[t.ID]
					if !ok {
						continue
					}
					name, ok := profileNames[mp.ProfileID]
					if !ok {
						continue
					}
					entry.MappableTags = append(entry.MappableTags, libraryMappableTagDTO{
						TagID: t.ID, TagLabel: t.Label, ProfileID: mp.ProfileID, ProfileName: name,
					})
				}
			}
			sort.Slice(entry.MappableTags, func(i, j int) bool {
				return entry.MappableTags[i].TagLabel < entry.MappableTags[j].TagLabel
			})
			rows := make([]libraryItemDTO, len(items))
			for i, it := range items {
				rows[i] = buildLibraryItem(inst, it, labels, mapped)
			}
			if deep {
				deepenLibraryItems(r.Context(), client, items, rows)
			}
			out.Items = append(out.Items, rows...)
			out.Instances = append(out.Instances, entry)
		}

		sort.Slice(out.Items, func(i, j int) bool {
			return out.Items[i].BitrateBps > out.Items[j].BitrateBps
		})
		cache.put(key, out)
		writeJSON(w, http.StatusOK, out)
	}
}

func buildLibraryItem(inst store.ArrInstanceRow, it arr.LibraryItem, labels map[int64]string, mapped map[int64]store.TagMappingRow) libraryItemDTO {
	row := libraryItemDTO{
		InstanceID:   inst.ID,
		InstanceName: inst.Name,
		ItemID:       it.ID,
		Title:        it.Title,
		Year:         it.Year,
		Path:         it.Path,
		FileCount:    it.FileCount,
		TotalSize:    it.TotalSize,
		TagLabels:    []string{},
		MappedTags:   []string{},
	}
	for _, tid := range it.TagIDs {
		label := labels[tid]
		if label == "" {
			label = strconv.FormatInt(tid, 10)
		}
		row.TagLabels = append(row.TagLabels, label)
		if _, ok := mapped[tid]; ok {
			row.Mapped = true
			row.MappedTags = append(row.MappedTags, label)
		}
	}
	sort.Strings(row.TagLabels)
	sort.Strings(row.MappedTags)
	row.RuntimeSeconds = it.RuntimeMinutes * 60 * it.FileCount
	row.BitrateBps = bitrate(it.TotalSize, row.RuntimeSeconds)
	return row
}

func deepenLibraryItems(ctx context.Context, client *arr.Client, items []arr.LibraryItem, rows []libraryItemDTO) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, libraryDeepWorker)
	for i := range items {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			files, err := client.Files(ctx, items[i].ID)
			if err != nil || len(files) == 0 {
				return
			}
			var size int64
			var seconds int
			codecs := map[string]int{}
			resolutions := map[string]int{}
			for _, f := range files {
				size += f.Size
				seconds += f.RuntimeSeconds
				if f.VideoCodec != "" {
					codecs[f.VideoCodec]++
				}
				if f.Resolution != "" {
					resolutions[f.Resolution]++
				}
			}
			rows[i].VideoCodec = mostCommon(codecs)
			rows[i].Resolution = mostCommon(resolutions)
			if seconds == 0 {
				return
			}
			if size > 0 {
				rows[i].TotalSize = size
			}
			rows[i].FileCount = len(files)
			rows[i].RuntimeSeconds = seconds
			rows[i].BitrateBps = bitrate(rows[i].TotalSize, seconds)
			rows[i].BitrateExact = true
		}(i)
	}
	wg.Wait()
}

func resolveLibraryTarget(w http.ResponseWriter, r *http.Request, st *store.Store) (*store.ArrInstanceRow, int64, bool) {
	kind, ok := parseArrKind(chi.URLParam(r, "kind"))
	if !ok {
		http.Error(w, "unknown kind", http.StatusBadRequest)
		return nil, 0, false
	}
	instanceID, err := strconv.ParseInt(chi.URLParam(r, "instanceId"), 10, 64)
	if err != nil {
		http.Error(w, "bad instance id", http.StatusBadRequest)
		return nil, 0, false
	}
	itemID, err := strconv.ParseInt(chi.URLParam(r, "itemId"), 10, 64)
	if err != nil {
		http.Error(w, "bad item id", http.StatusBadRequest)
		return nil, 0, false
	}
	inst, err := st.GetArrInstance(r.Context(), instanceID)
	if err != nil {
		http.Error(w, "instance not found", http.StatusNotFound)
		return nil, 0, false
	}
	if inst.Kind != kind {
		http.Error(w, "instance kind mismatch", http.StatusBadRequest)
		return nil, 0, false
	}
	return inst, itemID, true
}

func libraryAddTag(st *store.Store, cache *libraryCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inst, itemID, ok := resolveLibraryTarget(w, r, st)
		if !ok {
			return
		}
		var body struct {
			TagID int64 `json:"tagId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TagID <= 0 {
			http.Error(w, "bad tag id", http.StatusBadRequest)
			return
		}
		client := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey)
		tags, err := client.Tags(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if len(tags) == 0 {
			http.Error(w, fmt.Sprintf("%s has no tags — create one in %s first, then map it to a profile in Recodarr", inst.Name, inst.Kind), http.StatusConflict)
			return
		}
		label := ""
		for _, t := range tags {
			if t.ID == body.TagID {
				label = t.Label
				break
			}
		}
		if label == "" {
			http.Error(w, "tag does not exist on this instance", http.StatusBadRequest)
			return
		}
		mappings, err := st.ListTagMappingsByKind(r.Context(), inst.Kind)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mapping, ok := findTagMapping(mappings, body.TagID)
		if !ok {
			http.Error(w, "tag is not mapped to a HandBrake profile in Recodarr", http.StatusBadRequest)
			return
		}
		if _, err := st.GetProfile(r.Context(), mapping.ProfileID); err != nil {
			http.Error(w, "the profile this tag maps to no longer exists", http.StatusConflict)
			return
		}
		if err := client.AddTag(r.Context(), itemID, body.TagID); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		cache.markTagged(inst.Kind, inst.ID, itemID, label)
		writeJSON(w, http.StatusOK, map[string]any{"tagId": body.TagID, "tagLabel": label, "profileId": mapping.ProfileID})
	}
}

func findTagMapping(mappings []store.TagMappingRow, tagID int64) (store.TagMappingRow, bool) {
	for _, mp := range mappings {
		if mp.TagID == tagID {
			return mp, true
		}
	}
	return store.TagMappingRow{}, false
}

func libraryFiles(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inst, itemID, ok := resolveLibraryTarget(w, r, st)
		if !ok {
			return
		}
		files, err := arr.New(arr.Kind(inst.Kind), inst.URL, inst.APIKey).Files(r.Context(), itemID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := make([]libraryFileDTO, 0, len(files))
		for _, f := range files {
			row := libraryFileDTO{
				FileID:         f.ID,
				Path:           f.Path,
				RelativePath:   f.RelativePath,
				Size:           f.Size,
				RuntimeSeconds: f.RuntimeSeconds,
				VideoBitrate:   f.VideoBitrate,
				AudioBitrate:   f.AudioBitrate,
				VideoCodec:     f.VideoCodec,
				AudioCodec:     f.AudioCodec,
				Resolution:     f.Resolution,
				Quality:        f.Quality,
			}
			row.BitrateBps = bitrate(f.Size, f.RuntimeSeconds)
			row.BitrateExact = row.BitrateBps > 0
			out = append(out, row)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].BitrateBps > out[j].BitrateBps })
		writeJSON(w, http.StatusOK, out)
	}
}

func bitrate(size int64, seconds int) int64 {
	if size <= 0 || seconds <= 0 {
		return 0
	}
	return size * 8 / int64(seconds)
}

func mostCommon(counts map[string]int) string {
	best, bestN := "", 0
	for k, n := range counts {
		if n > bestN || (n == bestN && strings.Compare(k, best) < 0) {
			best, bestN = k, n
		}
	}
	return best
}
