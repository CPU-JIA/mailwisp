<script setup lang="ts">
import { computed } from 'vue'

import { buildSafeMessageDocument } from '../messageHTML'

const props = defineProps<{ html: string; title: string }>()

const source = computed(() => buildSafeMessageDocument(props.html))
</script>

<template>
  <iframe class="message-frame" sandbox="" referrerpolicy="no-referrer" :title="title" :srcdoc="source" />
</template>
