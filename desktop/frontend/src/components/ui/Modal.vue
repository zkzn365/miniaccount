<script setup>
import { X } from 'lucide-vue-next'

// 用原生 <dialog> 而不是引一个弹窗库：
// 它自带焦点陷阱、Esc 关闭、backdrop，行为比多数手写实现更正确，
// 而且不会为了一个弹窗再增加一条依赖。
defineProps({
  title: { type: String, default: '' },
  description: { type: String, default: '' },
  width: { type: String, default: 'max-w-3xl' },
})
const open = defineModel('open', { type: Boolean, default: false })
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="fixed inset-0 z-40 flex items-center justify-center bg-black/40 p-6"
      @click.self="open = false"
    >
      <div :class="['w-full rounded-xl border bg-card shadow-xl', width]">
        <div class="flex items-start gap-4 border-b p-5">
          <div class="flex-1">
            <h2 class="text-base font-semibold">{{ title }}</h2>
            <p v-if="description" class="mt-1 text-sm text-muted-foreground">{{ description }}</p>
          </div>
          <button class="text-muted-foreground hover:text-foreground" @click="open = false">
            <X class="size-4" />
          </button>
        </div>
        <div class="max-h-[70vh] overflow-auto p-5"><slot /></div>
        <div v-if="$slots.footer" class="flex justify-end gap-2 border-t p-4">
          <slot name="footer" />
        </div>
      </div>
    </div>
  </Teleport>
</template>
