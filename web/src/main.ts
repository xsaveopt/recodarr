import { createApp } from "vue";
import { createPinia } from "pinia";
import PrimeVue from "primevue/config";
import ToastService from "primevue/toastservice";
import ConfirmationService from "primevue/confirmationservice";
import Aura from "@primeuix/themes/aura";

import App from "./App.vue";
import { router } from "./router";

import "primeicons/primeicons.css";
import "./style.css";

const app = createApp(App);
app.use(createPinia());
app.use(router);
app.use(PrimeVue, {
  theme: {
    preset: Aura,
    options: {
      // Mirror the class set by useTheme() on <html>. PrimeVue components
      // re-themed via Aura follow this selector for dark mode.
      darkModeSelector: ".dark",
    },
  },
});
app.use(ToastService);
app.use(ConfirmationService);

app.mount("#app");
