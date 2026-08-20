<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import Tabs from "primevue/tabs";
import TabList from "primevue/tablist";
import Tab from "primevue/tab";
import TabPanels from "primevue/tabpanels";
import TabPanel from "primevue/tabpanel";

import LibraryPanel from "@/components/LibraryPanel.vue";

const route = useRoute();
const router = useRouter();
const validTabs = ["sonarr", "radarr"] as const;
type TabValue = (typeof validTabs)[number];

const activeTab = computed<TabValue>({
  get() {
    const t = route.query.tab as string | undefined;
    return (validTabs as readonly string[]).includes(t ?? "") ? (t as TabValue) : "sonarr";
  },
  set(v) {
    router.replace({ query: { ...route.query, tab: v === "sonarr" ? undefined : v } });
  },
});
</script>

<template>
  <section class="page">
    <header class="page-head">
      <h1 class="page-title">Library</h1>
      <p class="page-sub">
        What your library costs in disk space, ranked by bitrate — the fattest titles first, and
        whether Recodarr is already covering them.
      </p>
    </header>
    <Tabs v-model:value="activeTab">
      <TabList>
        <Tab value="sonarr">Sonarr</Tab>
        <Tab value="radarr">Radarr</Tab>
      </TabList>
      <TabPanels>
        <TabPanel value="sonarr"><LibraryPanel kind="sonarr" /></TabPanel>
        <TabPanel value="radarr"><LibraryPanel kind="radarr" /></TabPanel>
      </TabPanels>
    </Tabs>
  </section>
</template>
