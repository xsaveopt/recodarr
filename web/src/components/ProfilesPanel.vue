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
  const res = await notify.tryRun(
    () => Promise.all([api.profiles.list(), api.handbrake.caps()]),
    "Couldn't load profiles",
  );
  if (res) {
    items.value = res[0] ?? [];
    caps.value = res[1] ?? { encoders: [] };
  }
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

function defaultProfile(): Partial<Profile> {
  const first = caps.value.encoders[0]?.name ?? "x265";
  return {
    name: "",
    encoder: first,
    encoderPreset: "",
    encoderProfile: "",
    encoderTune: "",
    encoderLevel: "",
    quality: 22,
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
  };
}

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
      <Column header="Quality">
        <template #body="{ data }">RF {{ data.quality }}</template>
      </Column>
      <Column header="Resolution cap">
        <template #body="{ data }">{{ resolutionDisplay(data.maxWidth, data.maxHeight) }}</template>
      </Column>
      <Column header="Audio">
        <template #body="{ data }">{{ data.audioEncoder || "copy" }}</template>
      </Column>
      <Column header="Container" style="width: 5rem">
        <template #body="{ data }">{{ (data.containerFormat || 'mkv').toUpperCase() }}</template>
      </Column>
      <Column header="Options">
        <template #body="{ data }">
          <span v-if="data.subtitleCopy">subs </span>
          <span v-if="data.twoPass">2-pass </span>
          <span v-if="data.framerate">{{ data.framerate }}fps </span>
          <span v-if="data.extraArgs" title="Has extra args">args </span>
          <span v-if="!data.subtitleCopy && !data.twoPass && !data.framerate && !data.extraArgs">—</span>
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
      :style="{ width: '34rem' }"
    >
      <div v-if="editing" class="form">
        <div v-if="validationError" class="error">{{ validationError }}</div>

        <label>
          <span>Name</span>
          <InputText v-model="editing.name" placeholder="HEVC 1080p" />
        </label>

        <div class="section-title">Encoder</div>

        <label>
          <span>Encoder</span>
          <Select
            v-model="editing.encoder"
            :options="encoderOptions"
            optionLabel="label"
            optionValue="value"
            placeholder="Select encoder"
          />
        </label>
        <label>
          <span>Preset</span>
          <Select
            v-model="editing.encoderPreset"
            :options="[{ value: '', label: '— default —' }, ...presetOptions]"
            optionLabel="label"
            optionValue="value"
          />
        </label>
        <label>
          <span>Profile</span>
          <Select
            v-model="editing.encoderProfile"
            :options="[{ value: '', label: '— default —' }, ...profileOptions]"
            optionLabel="label"
            optionValue="value"
          />
        </label>
        <label>
          <span>Tune</span>
          <Select
            v-model="editing.encoderTune"
            :options="[{ value: '', label: '— none —' }, ...tuneOptions]"
            optionLabel="label"
            optionValue="value"
          />
        </label>
        <label>
          <span>Level</span>
          <Select
            v-model="editing.encoderLevel"
            :options="[{ value: '', label: '— auto —' }, ...levelOptions]"
            optionLabel="label"
            optionValue="value"
          />
        </label>
        <label>
          <span>Quality (RF)</span>
          <InputNumber v-model="editing.quality" :min="1" :max="51" showButtons />
        </label>

        <div class="section-title">Resolution cap</div>
        <p class="muted small">0 = no cap (keep original)</p>

        <label>
          <span>Max width (px)</span>
          <InputNumber v-model="editing.maxWidth" :min="0" placeholder="0" />
        </label>
        <label>
          <span>Max height (px)</span>
          <InputNumber v-model="editing.maxHeight" :min="0" placeholder="0" />
        </label>

        <div class="section-title">Audio</div>

        <label>
          <span>Audio encoder</span>
          <Select
            v-model="editing.audioEncoder"
            :options="audioEncoderOptions"
            optionLabel="label"
            optionValue="value"
          />
        </label>
        <label>
          <span>Audio bitrate (kbps)</span>
          <InputNumber v-model="editing.audioBitrate" :min="0" placeholder="0 = auto" />
        </label>
        <label>
          <span>Mixdown</span>
          <Select
            v-model="editing.audioMixdown"
            :options="audioMixdownOptions"
            optionLabel="label"
            optionValue="value"
          />
        </label>
        <p class="muted small">
          Empty encoder = copy all tracks (bitrate/mixdown ignored).
          Selecting an encoder re-encodes all audio tracks.
        </p>

        <label class="row">
          <span>Copy all subtitles</span>
          <ToggleSwitch v-model="editing.subtitleCopy" />
        </label>

        <div class="section-title">Encoding</div>

        <label class="row">
          <span>Two-pass encoding</span>
          <ToggleSwitch v-model="editing.twoPass" />
        </label>
        <p class="muted small">Better quality/size ratio; roughly doubles encode time.</p>

        <div class="section-title">Output</div>

        <label>
          <span>Container format</span>
          <Select
            v-model="editing.containerFormat"
            :options="containerOptions"
            optionLabel="label"
            optionValue="value"
          />
        </label>
        <label>
          <span>Framerate</span>
          <InputText v-model="editing.framerate" placeholder="e.g. 30, 24000/1001 (empty = source)" />
        </label>

        <div class="section-title">Advanced</div>

        <label>
          <span>Extra HandBrake args</span>
          <InputText v-model="editing.extraArgs" placeholder="--no-hwd-decode ..." class="mono" />
        </label>
        <p class="muted small">Appended verbatim to the HandBrakeCLI command. Use with care.</p>
      </div>
      <template #footer>
        <Button text label="Cancel" @click="editing = null" />
        <Button label="Save" icon="pi pi-check" @click="save" />
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
  color: #888;
  margin: 0;
  max-width: 50rem;
}
.small {
  font-size: 0.85rem;
}
.error {
  background: #fee;
  color: #900;
  padding: 0.5rem 0.75rem;
  border-radius: 4px;
}
.form {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.form label {
  display: grid;
  grid-template-columns: 10rem 1fr;
  align-items: center;
  gap: 0.75rem;
}
.form label.row {
  grid-template-columns: 10rem auto;
}
.section-title {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: #888;
  margin-top: 0.25rem;
}
:deep(.mono input) {
  font-family: monospace;
  font-size: 0.85rem;
}
</style>
