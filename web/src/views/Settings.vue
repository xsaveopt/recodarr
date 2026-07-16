<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import Tabs from "primevue/tabs";
import TabList from "primevue/tablist";
import Tab from "primevue/tab";
import TabPanels from "primevue/tabpanels";
import TabPanel from "primevue/tabpanel";

import ArrInstancesPanel from "@/components/ArrInstancesPanel.vue";
import QbitPanel from "@/components/QbitPanel.vue";
import ProfilesPanel from "@/components/ProfilesPanel.vue";
import MappingsPanel from "@/components/MappingsPanel.vue";
import WorkerPanel from "@/components/WorkerPanel.vue";
import NotificationsPanel from "@/components/NotificationsPanel.vue";
import LogsPanel from "@/components/LogsPanel.vue";
import AgentPanel from "@/components/AgentPanel.vue";

const route = useRoute();
const router = useRouter();
const validTabs = [
  "arr",
  "qbit",
  "profiles",
  "mappings",
  "worker",
  "notifications",
  "logs",
  "agent",
] as const;
type TabValue = (typeof validTabs)[number];

const activeTab = computed<TabValue>({
  get() {
    const t = route.query.tab as string | undefined;
    return (validTabs as readonly string[]).includes(t ?? "") ? (t as TabValue) : "arr";
  },
  set(v) {
    router.replace({ query: { ...route.query, tab: v === "arr" ? undefined : v } });
  },
});
</script>

<template>
  <section class="settings">
    <header class="page-head">
      <h1 class="page-title">Settings</h1>
      <p class="page-sub">Configure integrations, profiles, and the encoding worker.</p>
    </header>
    <Tabs v-model:value="activeTab">
      <TabList>
        <Tab value="arr">Sonarr / Radarr</Tab>
        <Tab value="qbit">qBittorrent</Tab>
        <Tab value="profiles">HandBrake Profiles</Tab>
        <Tab value="mappings">Mappings</Tab>
        <Tab value="worker">Worker</Tab>
        <Tab value="notifications">Notifications</Tab>
        <Tab value="logs">Logs</Tab>
        <Tab value="agent">Remote Agent</Tab>
      </TabList>
      <TabPanels>
        <TabPanel value="arr"><ArrInstancesPanel /></TabPanel>
        <TabPanel value="qbit"><QbitPanel /></TabPanel>
        <TabPanel value="profiles"><ProfilesPanel /></TabPanel>
        <TabPanel value="mappings"><MappingsPanel /></TabPanel>
        <TabPanel value="worker"><WorkerPanel /></TabPanel>
        <TabPanel value="notifications"><NotificationsPanel /></TabPanel>
        <TabPanel value="logs"><LogsPanel /></TabPanel>
        <TabPanel value="agent"><AgentPanel /></TabPanel>
      </TabPanels>
    </Tabs>
  </section>
</template>

<style scoped>
.settings {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}
.page-head {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
}
.page-title {
  margin: 0;
  font-size: 1.4rem;
  letter-spacing: -0.02em;
}
.page-sub {
  margin: 0;
  font-size: 0.85rem;
  color: var(--rc-muted);
}
</style>
