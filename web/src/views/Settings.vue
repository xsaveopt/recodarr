<script setup lang="ts">
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
</script>

<template>
  <section>
    <h2>Settings</h2>
    <Tabs value="arr">
      <TabList>
        <Tab value="arr">Sonarr / Radarr</Tab>
        <Tab value="qbit">qBittorrent</Tab>
        <Tab value="profiles">HandBrake Profiles</Tab>
        <Tab value="mappings">Mappings</Tab>
        <Tab value="worker">Worker</Tab>
        <Tab value="notifications">Notifications</Tab>
      </TabList>
      <TabPanels>
        <TabPanel value="arr"><ArrInstancesPanel /></TabPanel>
        <TabPanel value="qbit"><QbitPanel /></TabPanel>
        <TabPanel value="profiles"><ProfilesPanel /></TabPanel>
        <TabPanel value="mappings"><MappingsPanel /></TabPanel>
        <TabPanel value="worker"><WorkerPanel /></TabPanel>
        <TabPanel value="notifications"><NotificationsPanel /></TabPanel>
      </TabPanels>
    </Tabs>
  </section>
</template>
