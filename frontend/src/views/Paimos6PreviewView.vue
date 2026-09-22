<script setup lang="ts">
import { computed, defineAsyncComponent } from 'vue'
import { useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import HabitatControlRoom from '@/components/habitat/HabitatControlRoom.vue'
const route = useRoute()
const auth = useAuthStore()
const Sessions = defineAsyncComponent(() => import('./Paimos6SessionsView.vue'))
const sessions = computed(
  () => auth.user?.role !== 'reviewer' && (route.query.view === 'sessions' || typeof route.query.session === 'string'),
)
</script>
<template>
  <Sessions v-if="sessions" />
  <HabitatControlRoom v-else />
</template>
