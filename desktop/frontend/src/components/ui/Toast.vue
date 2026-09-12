<script setup>
import { notices, dismiss } from '@/lib/api'
import { X } from 'lucide-vue-next'

const tone = {
  success: 'border-[var(--profit)]/40 bg-[var(--profit)]/10',
  error: 'border-destructive/40 bg-destructive/10',
  warn: 'border-[var(--warn)]/40 bg-[var(--warn)]/10',
  info: 'border-border bg-card',
}
</script>

<template>
  <div class="pointer-events-none fixed bottom-4 right-4 z-50 flex w-[26rem] flex-col gap-2">
    <TransitionGroup
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="translate-y-2 opacity-0"
      leave-active-class="transition duration-150 ease-in"
      leave-to-class="opacity-0"
    >
      <div
        v-for="n in notices.items"
        :key="n.id"
        :class="['pointer-events-auto rounded-lg border p-3 shadow-lg backdrop-blur', tone[n.kind] ?? tone.info]"
      >
        <div class="flex items-start gap-2">
          <div class="flex-1">
            <p class="text-sm font-medium">{{ n.message }}</p>
            <!-- 细节必须能展开看：记账软件里「失败原因」往往就是账务线索本身 -->
            <pre
              v-if="n.detail"
              class="mt-1.5 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded bg-background/60 p-2 text-xs text-muted-foreground"
            >{{ n.detail }}</pre>
          </div>
          <button class="text-muted-foreground hover:text-foreground" @click="dismiss(n.id)">
            <X class="size-4" />
          </button>
        </div>
      </div>
    </TransitionGroup>
  </div>
</template>
