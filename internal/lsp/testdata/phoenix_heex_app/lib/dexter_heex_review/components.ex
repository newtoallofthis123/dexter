defmodule DexterHeexReview.Components do
  use Phoenix.Component

  attr(:title, :string, required: true)

  def remote_card(assigns) do
    ~H"""
    <article>{@title}</article>
    """
  end
end
