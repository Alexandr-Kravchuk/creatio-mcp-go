#pragma warning disable CS8618, // Non-nullable field is uninitialized.

using ATF.Repository;
using ATF.Repository.Attributes;
using System;
using System.Collections.Generic;
using System.Diagnostics.CodeAnalysis;
using System.ComponentModel.DataAnnotations;

namespace Acme.Models {
	/// <summary>
	/// Organizations the company works with.
	/// Customers, partners and suppliers.
	/// </summary>
	[ExcludeFromCodeCoverage]
	[Schema("Account")]
	public class Account: BaseModel {

		#region Schema Columns

		/// <summary>
		/// Account name
		/// </summary>
		[Required(ErrorMessage = "The {0} field is required.")]
		[MinLength(1, ErrorMessage = "The {0} field must be a minimum of {1} character long.")]
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[SchemaProperty("Name")]
		public string Name { get; set; }

		/// <summary>
		/// Code
		/// </summary>
		[MaxLength(50, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[SchemaProperty("Code")]
		public string Code { get; set; }

		[Required(ErrorMessage = "The {0} field is required.")]
		[MinLength(1, ErrorMessage = "The {0} field must be a minimum of {1} character long.")]
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[SchemaProperty("AlternativeName")]
		public string AlternativeName { get; set; }

		/// <summary>
		/// Owner
		/// </summary>
		[SchemaProperty("Owner")]
		public Guid OwnerId { get; set; }

		/// <inheritdoc cref="OwnerId"/>
		[LookupProperty("Owner")]
		public virtual Contact Owner { get; set; }

		/// <summary>
		/// Primary contact
		/// </summary>
		[SchemaProperty("PrimaryContact")]
		public Guid PrimaryContactId { get; set; }

		/// <inheritdoc cref="PrimaryContactId"/>
		[LookupProperty("PrimaryContact")]
		public virtual Contact PrimaryContact { get; set; }

		/// <summary>
		/// Type
		/// </summary>
		[SchemaProperty("Type")]
		public Guid TypeId { get; set; }

		/// <inheritdoc cref="TypeId"/>
		[LookupProperty("Type")]
		public virtual AccountType Type { get; set; }

		[SchemaProperty("Country")]
		public Guid CountryId { get; set; }

		[LookupProperty("Country")]
		public virtual Country Country { get; set; }

		/// <summary>
		/// Web
		/// </summary>
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[Url(ErrorMessage = "The {0} field is not a valid e-mail address.")]
		[SchemaProperty("Web")]
		public string Web { get; set; }

		/// <summary>
		/// Primary phone
		/// </summary>
		[Required(ErrorMessage = "The {0} field is required.")]
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[Phone(ErrorMessage = "The {0} field is not a valid e-mail address.")]
		[SchemaProperty("Phone")]
		public string Phone { get; set; }

		[MaxLength(500, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[SchemaProperty("Notes")]
		public string Notes { get; set; }

		/// <summary>
		/// Annual revenue
		/// </summary>
		[SchemaProperty("AnnualRevenue")]
		public decimal AnnualRevenue { get; set; }

		/// <summary>
		/// Logo
		/// </summary>
		[SchemaProperty("Logo")]
		public string Logo { get; set; }

		/// <summary>
		/// Created on
		/// </summary>
		[SchemaProperty("CreatedOn")]
		public DateTime CreatedOn { get; set; }

		/// <summary>
		/// Rating
		/// </summary>
		[SchemaProperty("Rating")]
		public decimal Rating { get; set; }

		/// <summary>
		/// Email
		/// </summary>
		[Required(ErrorMessage = "The {0} field is required.")]
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[EmailAddress(ErrorMessage = "The {0} field is not a valid e-mail address.")]
		[SchemaProperty("Email")]
		public string Email { get; set; }

		#endregion

		#region Details

		/// <summary>
		/// Collection of Contact by column AccountId
		/// <remarks>
		/// <see cref="global::Acme.Models.Contact">See more about the Contact model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.Contact.AccountId))]
		public virtual List<Contact> CollectionOfContactByAccount { get; set; }

		/// <summary>
		/// Collection of ContactCommunication by column Usr_AccountId
		/// <remarks>
		/// <see cref="global::Acme.Models.ContactCommunication">See more about the ContactCommunication model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.ContactCommunication.Usr_AccountId))]
		public virtual List<ContactCommunication> CollectionOfContactCommunicationByUsr_Account { get; set; }

		/// <summary>
		/// Collection of ContactCommunication by column UsrAccountId
		/// <remarks>
		/// <see cref="global::Acme.Models.ContactCommunication">See more about the ContactCommunication model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.ContactCommunication.UsrAccountId))]
		public virtual List<ContactCommunication> CollectionOfContactCommunicationByUsrAccount { get; set; }

		/// <summary>
		/// Collection of ContactCommunication by column usrAccountLinkId
		/// <remarks>
		/// <see cref="global::Acme.Models.ContactCommunication">See more about the ContactCommunication model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.ContactCommunication.usrAccountLinkId))]
		public virtual List<ContactCommunication> CollectionOfContactCommunicationByusrAccountLink { get; set; }

		#endregion
	}
}
#pragma warning restore CS8618 // Non-nullable field is uninitialized.
