<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import Button from "primevue/button";
import DataTable from "primevue/datatable";
import Column from "primevue/column";
import Dialog from "primevue/dialog";
import InputText from "primevue/inputtext";
import InputNumber from "primevue/inputnumber";
import Select from "primevue/select";
import ToggleSwitch from "primevue/toggleswitch";

import { api } from "@/api/client";
import { useNotify } from "@/composables/useNotify";
import type { EncoderCaps, HbCaps, Profile } from "@/types/api";

const notify = useNotify();
const items = ref<Profile[]>([]);
const caps = ref<HbCaps>({ encoders: [] });
const editing = ref<Partial<Profile> | null>(null);
const validationError = ref<string | null>(null);

const encoderOptions = computed(() =>
  caps.value.encoders.map((e) => ({ value: e.name, label: e.name })),
);

function capsForEncoder(name: string | undefined): EncoderCaps | undefined {
  return caps.value.encoders.find((e) => e.name === name);
}

const presetOptions = computed(() => {
  const ec = capsForEncoder(editing.value?.encoder);
  return ec?.presets?.map((p) => ({ value: p, label: p })) ?? [];
});

const profileOptions = computed(() => {
  const ec = capsForEncoder(editing.value?.encoder);
  return ec?.profiles?.map((p) => ({ value: p, label: p })) ?? [];
});

const tuneOptions = computed(() => {
  const ec = capsForEncoder(editing.value?.encoder);
  return ec?.tunes?.map((t) => ({ value: t, label: t })) ?? [];
});

const levelOptions = computed(() => {
  const ec = capsForEncoder(editing.value?.encoder);
  return ec?.levels?.map((l) => ({ value: l, label: l })) ?? [];
});

// Reset encoder-specific fields ONLY when the user changes encoder mid-edit. The initial
// transition undefined → <encoder name> happens when opening an existing profile, and
// must NOT wipe the saved preset/tune/profile/level values.
watch(
  () => editing.value?.encoder,
  (newEnc, oldEnc) => {
    if (!editing.value) return;
    if (oldEnc === undefined || newEnc === oldEnc) return;
    editing.value.encoderPreset = "";
    editing.value.encoderProfile = "";
    editing.value.encoderTune = "";
    editing.value.encoderLevel = "";
  },
);

async function load() {
  // Render the profiles table as soon as the DB query returns; HandBrake caps
  // (encoder discovery, slow on a cold cache because it shells out to
  // HandBrakeCLI per encoder) fills in behind the scenes. Caps are only
  // needed inside the edit dialog, so the table doesn't have to wait.
  const list = await notify.tryRun(() => api.profiles.list(), "Couldn't load profiles");
  if (list) items.value = list;

  const c = await notify.tryRun(() => api.handbrake.caps(), "Couldn't load HandBrake capabilities");
  if (c) caps.value = c;
}

const containerOptions = [
  { value: "mkv", label: "MKV (Matroska)" },
  { value: "mp4", label: "MP4" },
];

const audioEncoderOptions = [
  { value: "", label: "— copy all (default) —" },
  { value: "copy", label: "copy" },
  { value: "av_aac", label: "AAC (av_aac)" },
  { value: "fdk_aac", label: "AAC (fdk_aac)" },
  { value: "mp3", label: "MP3" },
  { value: "vorbis", label: "Vorbis" },
  { value: "opus", label: "Opus" },
  { value: "flac16", label: "FLAC 16-bit" },
  { value: "flac24", label: "FLAC 24-bit" },
  { value: "eac3", label: "E-AC-3" },
  { value: "ac3", label: "AC-3" },
];

const audioMixdownOptions = [
  { value: "", label: "— source channels —" },
  { value: "mono", label: "Mono" },
  { value: "stereo", label: "Stereo" },
  { value: "5point1", label: "5.1" },
  { value: "6point1", label: "6.1" },
  { value: "7point1", label: "7.1" },
];

const rateControlOptions = [
  { value: "crf", label: "CRF (constant quality)" },
  { value: "abr", label: "ABR (average bitrate)" },
];

function defaultProfile(): Partial<Profile> {
  const first = caps.value.encoders[0]?.name ?? "x265";
  return {
    name: "",
    encoder: first,
    encoderPreset: "",
    encoderProfile: "",
    encoderTune: "",
    encoderLevel: "",
    rateControl: "crf",
    quality: 22,
    videoBitrate: 2500,
    maxWidth: 0,
    maxHeight: 0,
    audioEncoder: "",
    audioBitrate: 0,
    audioMixdown: "",
    subtitleCopy: true,
    twoPass: false,
    containerFormat: "mkv",
    extraArgs: "",
    framerate: "",
    skipCodecs: "",
    skipBitrateMBPerHour: 0,
    skipFileSizeMB: 0,
    skipDurationMinutes: 0,
    skipHeightPx: 0,
    skipHDR: false,
    bloatPolicy: "off",
    bloatRetryMax: 3,
    bloatRetryStep: 3,
    bloatMinSavingsPercent: 0,
  };
}

const bloatPolicyOptions = [
  { value: "off", label: "Off — always keep encoded file" },
  { value: "keep_original", label: "Keep original if encode is larger" },
  { value: "retry_higher_crf", label: "Retry with higher CRF, then keep original" },
];

function startCreate() {
  validationError.value = null;
  editing.value = defaultProfile();
}

function startEdit(row: Profile) {
  validationError.value = null;
  editing.value = { ...row };
}

async function save() {
  if (!editing.value) return;
  validationError.value = null;
  if (!editing.value.name?.trim()) {
    validationError.value = "Name is required.";
    return;
  }
  if (!editing.value.encoder) {
    validationError.value = "Encoder is required.";
    return;
  }
  const ok = await notify.tryRun(
    () => api.profiles.upsert(editing.value as Profile),
    "Couldn't save profile",
  );
  if (ok !== undefined) {
    notify.success(`Saved profile "${editing.value!.name}"`);
    editing.value = null;
    await load();
  }
}

function remove(row: Profile) {
  notify.confirmDelete({
    name: row.name,
    onAccept: async () => {
      const ok = await notify.tryRun(() => api.profiles.remove(row.id), "Couldn't delete profile");
      if (ok !== undefined) {
        notify.success(`Deleted profile "${row.name}"`);
        await load();
      }
    },
  });
}

function resolutionDisplay(w: number, h: number) {
  if (!w && !h) return "Original";
  const parts = [];
  if (w) parts.push(`${w}w`);
  if (h) parts.push(`${h}h`);
  return parts.join(" / ");
}

onMounted(load);
</script>

<template>
  <div class="panel">
    <div class="panel-head">
      <p class="muted">
        Encoding profiles. Encoder options are read directly from the HandBrakeCLI binary.
      </p>
      <Button icon="pi pi-plus" label="Add" @click="startCreate" />
    </div>
    <DataTable :value="items" stripedRows size="small">
      <Column field="name" header="Name" />
      <Column field="encoder" header="Encoder" />
      <Column header="Rate">
        <template #body="{ data }">
          <span v-if="data.rateControl === 'abr'">{{ data.videoBitrate || 0 }} kbps</span>
          <span v-else>RF {{ data.quality }}</span>
        </template>
      </Column>
      <Column header="Resolution cap">
        <template #body="{ data }">{{ resolutionDisplay(data.maxWidth, data.maxHeight) }}</template>
      </Column>
      <Column header="Audio">
        <template #body="{ data }">{{ data.audioEncoder || "copy" }}</template>
      </Column>
      <Column header="Container" style="width: 5rem">
        <template #body="{ data }">{{ (data.containerFormat || "mkv").toUpperCase() }}</template>
      </Column>
      <Column header="Options">
        <template #body="{ data }">
          <span v-if="data.subtitleCopy">subs </span>
          <span v-if="data.twoPass">2-pass </span>
          <span v-if="data.framerate">{{ data.framerate }}fps </span>
          <span v-if="data.extraArgs" title="Has extra args">args </span>
          <span v-if="!data.subtitleCopy && !data.twoPass && !data.framerate && !data.extraArgs"
            >—</span
          >
        </template>
      </Column>
      <Column header="" style="width: 8rem">
        <template #body="{ data }">
          <Button text size="small" icon="pi pi-pencil" @click="startEdit(data)" />
          <Button text size="small" severity="danger" icon="pi pi-trash" @click="remove(data)" />
        </template>
      </Column>
    </DataTable>

    <Dialog
      :visible="editing !== null"
      @update:visible="(v) => (editing = v ? editing : null)"
      modal
      :header="editing?.id ? 'Edit profile' : 'New profile'"
      :style="{ width: '76rem' }"
      :breakpoints="{ '1100px': '95vw' }"
      class="profile-dialog"
    >
      <div v-if="editing" class="editor">
        <div v-if="validationError" class="error">{{ validationError }}</div>

        <!-- Top strip: name + a couple of headline knobs always in sight -->
        <section class="block block-head">
          <label class="field field-wide">
            <span>Profile name</span>
            <InputText v-model="editing.name" placeholder="HEVC 1080p" />
          </label>
          <label class="field">
            <span>Container</span>
            <Select
              v-model="editing.containerFormat"
              :options="containerOptions"
              optionLabel="label"
              optionValue="value"
            />
          </label>
          <label class="field field-narrow">
            <span>Rate control</span>
            <Select
              v-model="editing.rateControl"
              :options="rateControlOptions"
              optionLabel="label"
              optionValue="value"
            />
          </label>
          <label v-if="editing.rateControl !== 'abr'" class="field field-narrow">
            <span>Quality (RF)</span>
            <InputNumber v-model="editing.quality" :min="1" :max="51" showButtons />
          </label>
          <label v-else class="field field-narrow">
            <span>Bitrate (kbps)</span>
            <InputNumber
              v-model="editing.videoBitrate"
              :min="100"
              :max="100000"
              :step="100"
              showButtons
            />
          </label>
        </section>

        <div class="grid">
          <!-- VIDEO ENCODER -->
          <section class="block">
            <h3 class="block-title">Video encoder</h3>
            <div class="fields">
              <label class="field">
                <span>Encoder</span>
                <Select
                  v-model="editing.encoder"
                  :options="encoderOptions"
                  optionLabel="label"
                  optionValue="value"
                  placeholder="Select encoder"
                />
              </label>
              <label class="field">
                <span>Preset</span>
                <Select
                  v-model="editing.encoderPreset"
                  :options="[{ value: '', label: '— default —' }, ...presetOptions]"
                  optionLabel="label"
                  optionValue="value"
                />
              </label>
              <label class="field">
                <span>Profile</span>
                <Select
                  v-model="editing.encoderProfile"
                  :options="[{ value: '', label: '— default —' }, ...profileOptions]"
                  optionLabel="label"
                  optionValue="value"
                />
              </label>
              <label class="field">
                <span>Tune</span>
                <Select
                  v-model="editing.encoderTune"
                  :options="[{ value: '', label: '— none —' }, ...tuneOptions]"
                  optionLabel="label"
                  optionValue="value"
                />
              </label>
              <label class="field">
                <span>Level</span>
                <Select
                  v-model="editing.encoderLevel"
                  :options="[{ value: '', label: '— auto —' }, ...levelOptions]"
                  optionLabel="label"
                  optionValue="value"
                />
              </label>
              <label class="field field-toggle">
                <span>Two-pass</span>
                <span class="toggle-row">
                  <ToggleSwitch v-model="editing.twoPass" />
                  <span class="muted hint">Better quality/size ratio, ~2× encode time</span>
                </span>
              </label>
            </div>
          </section>

          <!-- VIDEO OUTPUT -->
          <section class="block">
            <h3 class="block-title">Video output</h3>
            <div class="fields">
              <label class="field">
                <span>Max width</span>
                <InputNumber
                  v-model="editing.maxWidth"
                  :min="0"
                  placeholder="0 = no cap"
                  suffix=" px"
                />
              </label>
              <label class="field">
                <span>Max height</span>
                <InputNumber
                  v-model="editing.maxHeight"
                  :min="0"
                  placeholder="0 = no cap"
                  suffix=" px"
                />
              </label>
              <label class="field">
                <span>Framerate</span>
                <InputText
                  v-model="editing.framerate"
                  placeholder="e.g. 30, 24000/1001 (empty = source)"
                />
              </label>
            </div>
          </section>

          <!-- AUDIO -->
          <section class="block">
            <h3 class="block-title">Audio</h3>
            <div class="fields">
              <label class="field">
                <span>Encoder</span>
                <Select
                  v-model="editing.audioEncoder"
                  :options="audioEncoderOptions"
                  optionLabel="label"
                  optionValue="value"
                />
              </label>
              <label class="field">
                <span>Bitrate</span>
                <InputNumber
                  v-model="editing.audioBitrate"
                  :min="0"
                  placeholder="0 = auto"
                  suffix=" kbps"
                />
              </label>
              <label class="field">
                <span>Mixdown</span>
                <Select
                  v-model="editing.audioMixdown"
                  :options="audioMixdownOptions"
                  optionLabel="label"
                  optionValue="value"
                />
              </label>
              <label class="field field-toggle">
                <span>Subtitles</span>
                <span class="toggle-row">
                  <ToggleSwitch v-model="editing.subtitleCopy" />
                  <span class="muted hint">Copy all subtitle tracks</span>
                </span>
              </label>
            </div>
            <p class="muted hint span-2">
              Empty encoder = copy all audio tracks (bitrate/mixdown ignored).
            </p>
          </section>

          <!-- ADVANCED -->
          <section class="block">
            <h3 class="block-title">Advanced</h3>
            <div class="fields">
              <label class="field field-wide">
                <span>Extra args</span>
                <InputText
                  v-model="editing.extraArgs"
                  placeholder="--no-hwd-decode ..."
                  class="mono"
                />
              </label>
            </div>
            <p class="muted hint">Appended verbatim to the HandBrakeCLI command. Use with care.</p>
          </section>

          <!-- SIZE GUARD — post-encode policy -->
          <section class="block block-full">
            <div class="block-title-row">
              <h3 class="block-title">Size guard</h3>
              <span class="muted hint">
                What to do when the encode produces a file that didn't shrink. Useful when
                re-encoding already-efficient sources where a hardware encoder may bloat the file.
              </span>
            </div>
            <div class="filter-grid">
              <label class="field field-wide">
                <span>Policy</span>
                <Select
                  v-model="editing.bloatPolicy"
                  :options="bloatPolicyOptions"
                  optionLabel="label"
                  optionValue="value"
                />
              </label>
              <label class="field">
                <span>Min savings required</span>
                <InputNumber
                  v-model="editing.bloatMinSavingsPercent"
                  :min="0"
                  :max="50"
                  :step="1"
                  suffix=" %"
                  placeholder="0"
                  :disabled="editing.bloatPolicy === 'off'"
                />
              </label>
              <label class="field">
                <span>Retry max</span>
                <InputNumber
                  v-model="editing.bloatRetryMax"
                  :min="0"
                  :max="10"
                  :step="1"
                  suffix=" tries"
                  :disabled="editing.bloatPolicy !== 'retry_higher_crf'"
                />
              </label>
              <label class="field">
                <span>Retry step</span>
                <InputNumber
                  v-model="editing.bloatRetryStep"
                  :min="1"
                  :max="20"
                  :step="1"
                  suffix=" CRF"
                  :disabled="editing.bloatPolicy !== 'retry_higher_crf'"
                />
              </label>
            </div>
            <p class="muted hint span-2">
              <strong v-if="editing.bloatPolicy === 'off'">Off:</strong>
              <strong v-else-if="editing.bloatPolicy === 'keep_original'">Keep original:</strong>
              <strong v-else>Retry:</strong>
              <span v-if="editing.bloatPolicy === 'off'">
                The encoded file always replaces the source, even if it grew. Same as before this
                feature existed.
              </span>
              <span v-else-if="editing.bloatPolicy === 'keep_original'">
                After every encode the new file's size is compared to the original. If it's not at
                least <code>{{ editing.bloatMinSavingsPercent || 0 }}%</code> smaller, the new
                file is discarded and the source is kept untouched. The job is marked
                <code>skipped</code> with the size info in its reason.
              </span>
              <span v-else>
                Same compare as Keep original, but if the encode is too big we retry with
                <code>CRF + {{ editing.bloatRetryStep }}</code> up to
                <code>{{ editing.bloatRetryMax }}</code> times before giving up and keeping the
                source. Each retry is a full encode — set Retry max with that in mind.
              </span>
            </p>
          </section>

          <!-- PRE-ENCODE FILTERS — full-width, denser grid -->
          <section class="block block-full">
            <div class="block-title-row">
              <h3 class="block-title">Pre-encode filters</h3>
              <span class="muted hint">
                Skip a job before encoding when its source matches any rule below. Marks the job as
                <code>skipped</code> with the reason. Leave a field at <code>0</code> or blank to
                disable. All checks except file size require <code>ffprobe</code>.
              </span>
            </div>
            <div class="filter-grid">
              <label class="field">
                <span>Codec in</span>
                <InputText
                  v-model="editing.skipCodecs"
                  placeholder="e.g. av1,hevc"
                  class="mono"
                />
              </label>
              <label class="field">
                <span>Bitrate ≤</span>
                <InputNumber
                  v-model="editing.skipBitrateMBPerHour"
                  :min="0"
                  :step="100"
                  suffix=" MB/h"
                  placeholder="0"
                />
              </label>
              <label class="field">
                <span>File size ≤</span>
                <InputNumber
                  v-model="editing.skipFileSizeMB"
                  :min="0"
                  :step="50"
                  suffix=" MB"
                  placeholder="0"
                />
              </label>
              <label class="field">
                <span>Duration ≤</span>
                <InputNumber
                  v-model="editing.skipDurationMinutes"
                  :min="0"
                  :step="1"
                  suffix=" min"
                  placeholder="0"
                />
              </label>
              <label class="field">
                <span>Height ≤</span>
                <InputNumber
                  v-model="editing.skipHeightPx"
                  :min="0"
                  :step="120"
                  suffix=" px"
                  placeholder="0"
                />
              </label>
              <label class="field field-toggle">
                <span>HDR</span>
                <span class="toggle-row">
                  <ToggleSwitch v-model="editing.skipHDR" />
                  <span class="muted hint">PQ / HLG sources</span>
                </span>
              </label>
            </div>
          </section>
        </div>
      </div>
      <template #footer>
        <Button text label="Cancel" @click="editing = null" />
        <Button label="Save profile" icon="pi pi-check" @click="save" />
      </template>
    </Dialog>
  </div>
</template>

<style scoped>
.panel {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}
.muted {
  color: var(--app-muted);
  margin: 0;
  max-width: 50rem;
}
.small {
  font-size: 0.85rem;
}
.error {
  background: var(--app-error-bg);
  color: var(--app-error-fg);
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
}
/* Power-user editor layout. Card per logical group, two-column at desktop,
   one-column at narrower breakpoints (Dialog already drops to 95vw at <1100px). */
.editor {
  display: flex;
  flex-direction: column;
  gap: 0.85rem;
}

/* Top strip: name + container + quality always in sight */
.block-head {
  display: grid;
  grid-template-columns: 1.6fr 0.9fr 0.7fr;
  gap: 0.85rem;
  align-items: end;
}

/* The two-column card grid for the body. Most cards span one column;
   .block-full spans both. */
.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.85rem;
}
.block-full {
  grid-column: 1 / -1;
}
@media (max-width: 900px) {
  .grid {
    grid-template-columns: 1fr;
  }
  .block-head {
    grid-template-columns: 1fr;
  }
}

/* Card primitive */
.block {
  background: var(--rc-surface);
  border: 1px solid var(--rc-border);
  border-radius: var(--rc-r-md);
  padding: 0.85rem 1rem 1rem;
}
.block-title {
  margin: 0 0 0.65rem;
  font-size: 0.72rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--rc-muted);
}
.block-title-row {
  display: flex;
  align-items: baseline;
  gap: 0.85rem;
  margin-bottom: 0.65rem;
  flex-wrap: wrap;
}
.block-title-row .block-title {
  margin: 0;
  flex-shrink: 0;
}
.block-title-row .hint {
  flex: 1;
  min-width: 18rem;
}

/* Two-column field grid inside each card */
.fields {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.6rem 0.85rem;
}
.filter-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 0.6rem 0.85rem;
}
@media (max-width: 700px) {
  .fields,
  .filter-grid {
    grid-template-columns: 1fr;
  }
}

/* A single field: small uppercase label above its input */
.field {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  min-width: 0;
}
.field > span {
  font-size: 0.72rem;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--rc-muted);
}
.field-wide {
  grid-column: span 2;
}
.field-narrow :deep(.p-inputnumber-input) {
  width: 5rem;
}
.field-toggle {
  justify-content: flex-end;
}

/* Make every input fill its slot so the grid columns line up cleanly */
.field :deep(.p-inputtext),
.field :deep(.p-inputnumber),
.field :deep(.p-inputnumber-input),
.field :deep(.p-select) {
  width: 100%;
}

.toggle-row {
  display: inline-flex;
  align-items: center;
  gap: 0.6rem;
  min-height: 28px;
}
.hint {
  font-size: 0.78rem;
  color: var(--rc-muted);
  margin: 0;
  line-height: 1.4;
}
.hint code {
  background: var(--rc-code-bg);
  padding: 0.05rem 0.3rem;
  border-radius: var(--rc-r-sm);
  font-size: 0.78rem;
}
.span-2 {
  margin-top: 0.5rem;
}

:deep(.mono input) {
  font-family: var(--rc-font-mono);
  font-size: 0.82rem;
}

/* Reserve dialog content area so it scrolls cleanly on short viewports */
:deep(.profile-dialog .p-dialog-content) {
  max-height: 80vh;
  overflow-y: auto;
}
</style>
