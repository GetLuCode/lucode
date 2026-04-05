import { useRef } from 'react'
import { Listbox } from '@headlessui/react'
import { IconPaperclip } from '@tabler/icons-react'

interface Props {
  onImageSelect: (base64: string, mediaType: string) => void
  disabled: boolean
}

export function AttachmentButton({ onImageSelect, disabled }: Props) {
  const fileInputRef = useRef<HTMLInputElement>(null)

  const handleImageSelect = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return

    // Validate image type
    const validTypes = ['image/jpeg', 'image/png', 'image/gif', 'image/webp']
    if (!validTypes.includes(file.type)) {
      alert('Only JPEG, PNG, GIF, and WebP images are supported')
      return
    }

    // Convert to base64
    const reader = new FileReader()
    reader.onload = (event) => {
      const base64 = (event.target?.result as string).split(',')[1]
      onImageSelect(base64, file.type)
    }
    reader.readAsDataURL(file)

    // Reset input
    if (fileInputRef.current) {
      fileInputRef.current.value = ''
    }
  }

  return (
    <div className="attachment-button">
      <Listbox disabled={disabled}>
        <div className="listbox-wrapper">
          <Listbox.Button className="attachment-btn">
            <IconPaperclip size={18} />
          </Listbox.Button>
          <Listbox.Options className="attachment-menu">
            <Listbox.Option value="image" className="attachment-option">
              <label className="attachment-label">
                Image
                <input
                  ref={fileInputRef}
                  type="file"
                  accept="image/jpeg,image/png,image/gif,image/webp"
                  onChange={handleImageSelect}
                  style={{ display: 'none' }}
                />
              </label>
            </Listbox.Option>
          </Listbox.Options>
        </div>
      </Listbox>
    </div>
  )
}
