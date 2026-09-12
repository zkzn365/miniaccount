<script setup>
defineProps({
  title: { type: String, default: '暂无数据' },
  description: { type: String, default: '' },
})
</script>

<template>
  <div class="flex flex-col items-center justify-center gap-2 py-16 text-center">
    <p class="text-sm font-medium text-muted-foreground">{{ title }}</p>
    <p v-if="description" class="max-w-md text-xs text-muted-foreground/70">{{ description }}</p>
    <div v-if="$slots.default" class="mt-2"><slot /></div>
  </div>
</template>
