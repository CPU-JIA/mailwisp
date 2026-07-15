<script setup lang="ts">
import { computed } from 'vue'

import { buildSafeMessageDocument } from '../messageHTML'

const props = defineProps<{ html: string; title: string; cidSources?: Record<string, string> }>()

const source = computed(() => buildSafeMessageDocument(props.html, props.cidSources))
</script>

<template>
  <iframe class="message-frame" sandbox="" referrerpolicy="no-referrer" :title="title" :srcdoc="source" />
</template>
