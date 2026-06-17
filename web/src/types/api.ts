export type ArrKind = "sonarr" | "radarr";

export interface ArrInstance {
  id: number;
  kind: ArrKind;
  name: string;
  url: string;
  apiKey?: string;
  webhookSecret?: string;
  enabled: boolean;
  hasApiKey: boolean;
  hasWebhookSecret: boolean;
  deleted: boolean;
}

export interface QbitInstance {
  id: number;
  name: string;
  url: string;
  username: string;
  password?: string;
  hasPassword: boolean;
}

export interface Profile {
  id: number;
  name: string;
  encoder: string;
  encoderPreset: string;
  encoderProfile: string;
  encoderTune: string;
  encoderLevel: string;
  rateControl: "crf" | "abr";
  quality: number;
  videoBitrate: number;
  maxWidth: number;
  maxHeight: number;
  audioEncoder: string;
  audioBitrate: number;
  audioMixdown: string;
  audioBitratesByChannels: Record<string, number>;
  subtitleCopy: boolean;
  twoPass: boolean;
  containerFormat: string;
  extraArgs: string;
  framerate: string;
  skipCodecs: string;
  skipBitrateMBPerHour: number;
  skipBitrateUnit: "mb_per_hour" | "kbps";
  skipFileSizeMB: number;
  skipDurationMinutes: number;
  skipHeightPx: number;
  skipHDR: boolean;
  bloatPolicy: "off" | "keep_original" | "retry_higher_crf";
  bloatRetryMax: number;
  bloatRetryStep: number;
  bloatMinSavingsPercent: number;
  deleted: boolean;
}

export interface TagMapping {
  id: number;
  arrKind: "sonarr" | "radarr" | "both";
  tagId: number;
  tagLabel: string;
  profileId: number;
}

export interface InstanceTag {
  instanceId: number;
  instanceName: string;
  kind: "sonarr" | "radarr";
  tagId: number;
  tagLabel: string;
}

export interface EncoderCaps {
  name: string;
  presets: string[];
  profiles: string[];
  tunes: string[];
  levels: string[];
}

export interface HbCaps {
  encoders: EncoderCaps[];
}

export interface DebugInfo {
  hbVersion: string;
  hbFound: boolean;
  encoders: string[];
  vaapiAvailable: boolean;
  qsvAvailable: boolean;
  nvencAvailable: boolean;
  platform: string;
  arch: string;
}

export type JobStatus =
  | "waiting_for_seed"
  | "waiting_for_hardlink"
  | "ready"
  | "encoding"
  | "done"
  | "failed"
  | "skipped";

export interface Job {
  id: number;
  arrKind: ArrKind;
  arrInstanceId: number;
  arrItemId: number;
  arrParentId: number;
  title: string;
  filePath: string;
  fileSize: number;
  downloadId: string;
  profileId: number | null;
  status: JobStatus;
  error?: string;
  encodeLog?: string;
  refreshError?: string;
  attempts: number;
  createdAt: string;
  updatedAt: string;
  startedAt?: string;
  finishedAt?: string;
  originalSize?: number;
  finalSize?: number;
  source: "webhook" | "backfill";
}

export interface LibraryItem {
  itemId: number;
  title: string;
  path: string;
  tagId: number;
  tagLabel: string;
  profileId: number;
  profileName: string;
  fileCount: number;
  totalSize: number;
  activeJobs: number;
  doneJobs: number;
}

export interface LibraryResponse {
  items: LibraryItem[];
  noMappings: boolean;
}

export interface LibraryQueueResponse {
  inserted: number;
  skipped: number;
  errors?: string[];
}

export interface ScanItem {
  itemId: number;
  title: string;
  path: string;
  tagLabel: string;
  profileName: string;
  fileCount: number;
  encodedCount: number;
  queuedCount: number;
  unencodedCount: number;
}

export interface ScanResponse {
  items: ScanItem[];
  noMappings: boolean;
  suffixDisabled: boolean;
}

export interface JobListParams {
  status?: string;
  kind?: string;
  profileId?: number;
  q?: string;
  limit?: number;
  offset?: number;
  order?: "asc" | "desc";
  sort?: "id" | "updated";
}

export interface JobsPage {
  total: number;
  limit: number;
  offset: number;
  jobs: Job[];
}

export interface JobDebug {
  jobId: number;
  status: JobStatus;
  downloadId: string;
  downloadIdLength: number;
  filePath: string;
  qbit: {
    configured: boolean;
    url?: string;
    reachable: boolean;
    loginError?: string;
    lookup?: {
      found: boolean;
      hash?: string;
      name?: string;
      state?: string;
      progress?: number;
      category?: string;
      savePath?: string;
    };
    lookupError?: string;
  };
  attempts: number;
  waitingForSeedCount: number;
  seedCheckBatchLimit: number;
  stalledReason?: string;
  encode?: {
    profileId?: number;
    profileName?: string;
    profileEncoder?: string;
    originalBytes?: number;
    finalBytes?: number;
    savedBytes?: number;
    savedPercent?: number;
    startedAt?: string;
    finishedAt?: string;
    durationSeconds?: number;
    error?: string;
    refreshError?: string;
  };
}

export interface WorkerStatus {
  isEncoding: boolean;
  encodingJobId: number;
  encodingJobIds: number[];
  progress: ProgressEvent[];
  lastTickAt: string | null;
  window: WindowStatus;
  maxParallelEncodes: number;
  paused: boolean;
}

export interface ProgressEvent {
  jobId: number;
  title: string;
  percent: number;
  fps: number;
  eta: string;
}

export interface WindowStatus {
  start: string;
  end: string;
  active: boolean;
  hasLimit: boolean;
}

export interface JobStats {
  waitingForSeed: number;
  waitingForHardlink: number;
  ready: number;
  encoding: number;
  done: number;
  failed: number;
  skipped: number;
  totalSavedBytes: number;
}

export type HealthLevel = "error" | "warn";

export interface HealthIssue {
  level: HealthLevel;
  source: string;
  title: string;
  detail?: string;
}

export interface HealthSnapshot {
  ok: boolean;
  issues: HealthIssue[];
  checkedAt: string;
}

export interface AppSettings {
  worker_interval_seconds?: string;
  max_parallel_encodes?: string;
  encoding_window_start?: string;
  encoding_window_end?: string;
  notify_url?: string;
  notify_on_done?: string;
  notify_on_fail?: string;
  notify_on_health?: string;
  log_app_level?: string;
  log_rotate_enabled?: string;
  log_max_size_mb?: string;
  log_max_age_days?: string;
  log_max_backups?: string;
  log_compress?: string;
  agent_enabled?: string;
  agent_url?: string;
  agent_token?: string;
  agent_fallback_local?: string;
  hasAgentToken?: string;
  [key: string]: string | undefined;
}
