// Dropdown of PXE-server images for deploy/inspect. Defaults to the server's
// current default entry; picking a non-default one asks the driver to flip the
// PXE default for that boot. Renders nothing when selection is unavailable
// (driver has no --pxe-root) or there's only one image (nothing to choose).
import { useEffect, useState } from 'react'
import { listPxeImages, PxeImage } from '../api/deploy'

export type ImagePickerProps = {
  // onChange receives the picked image name, or '' when it's the default
  // (so the caller sends no image and the driver skips the flip).
  onChange: (image: string) => void
}

export const ImagePicker = (props: ImagePickerProps) => {
  const { onChange } = props
  const [images, setImages] = useState<PxeImage[]>([])
  const [selected, setSelected] = useState<string>('')

  useEffect(() => {
    listPxeImages()
      .then((imgs) => {
        setImages(imgs)
        const def = imgs.find((i) => i.default)?.name ?? imgs[0]?.name ?? ''
        setSelected(def)
      })
      .catch(() => {})
  }, [])

  if (images.length < 2) return null // nothing to choose (or selection disabled)

  const defaultName = images.find((i) => i.default)?.name ?? ''
  return (
    <label className="flex flex-col gap-y-1">
      <span className="secondary-body5 font-medium text-functional-text-secondary">
        Boot image
      </span>
      <select
        className="primary-body4 rounded-md border border-functional-border-divider px-3 py-2 outline-none focus:border-primary"
        value={selected}
        onChange={(e) => {
          const v = e.target.value
          setSelected(v)
          onChange(v === defaultName ? '' : v)
        }}
      >
        {images.map((i) => (
          <option key={i.name} value={i.name}>
            {i.name}
            {i.default ? ' (default)' : ''}
          </option>
        ))}
      </select>
      {selected !== defaultName && (
        <span className="secondary-body5 text-functional-text-light">
          The PXE default is temporarily repointed to this image for the boot,
          then restored.
        </span>
      )}
    </label>
  )
}
