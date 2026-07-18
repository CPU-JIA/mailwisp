<script setup lang="ts">
import { computed } from 'vue'

import { buildSafeMessageDocument } from '../messageHTML'

const props = defineProps<{ html: string; title?: string; cidSources?: Record<string, string> }>()

const source = computed(() => buildSafeMessageDocument(props.html, props.cidSources))
const frameTitle = computed(() => props.title?.trim() || 'MailWisp message')
</script>

<template>
  <iframe class="message-frame" sandbox="" referrerpolicy="no-referrer" :title="frameTitle" :srcdoc="source" />
</template>
